package awsapi

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// XML building blocks for awsQuery / ec2Query / restXml responses.
//
//	map[string]any  elements named by key (sorted)
//	Ordered         elements in the given order
//	Members         a list; each value in a <member> element (awsQuery)
//	Items           a list; each value in an <item> element (ec2Query)
//	Flat            a flattened list: each value repeats the parent element
//	Named           a list whose elements have a custom name
//	time.Time       ISO 8601 (2006-01-02T15:04:05.000Z)
//	Raw             pre-rendered XML
//	nil             the element is omitted
type (
	Ordered []KV
	KV      struct {
		K string
		V any
	}
	Members []any
	Items   []any
	Flat    []any
	Named   struct {
		Name   string
		Values []any
	}
	Raw string
	// NoResult makes an awsQuery response without a <OpResult> element.
	NoResult struct{}
)

// Strings converts a string slice to list values.
func Strings(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// WriteXML renders v as the children of <name> into b.
func WriteXML(b *strings.Builder, name string, v any) {
	if v == nil {
		return
	}
	switch t := v.(type) {
	case Flat:
		for _, x := range t {
			WriteXML(b, name, x)
		}
		return
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return
		}
		v = rv.Elem().Interface()
	}
	b.WriteString("<" + name + ">")
	writeInner(b, v)
	b.WriteString("</" + name + ">")
}

func writeInner(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
	case Raw:
		b.WriteString(string(t))
	case string:
		_ = xml.EscapeText(b, []byte(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case int, int32, int64, uint, uint32, uint64:
		fmt.Fprintf(b, "%d", t)
	case float32:
		b.WriteString(strconv.FormatFloat(float64(t), 'f', -1, 32))
	case float64:
		b.WriteString(strconv.FormatFloat(t, 'f', -1, 64))
	case time.Time:
		b.WriteString(t.UTC().Format("2006-01-02T15:04:05.000Z"))
	case *time.Time:
		if t != nil {
			b.WriteString(t.UTC().Format("2006-01-02T15:04:05.000Z"))
		}
	case Time:
		b.WriteString(t.UTC().Format("2006-01-02T15:04:05.000Z"))
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			WriteXML(b, k, t[k])
		}
	case map[string]string:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			WriteXML(b, k, t[k])
		}
	case Ordered:
		for _, kv := range t {
			WriteXML(b, kv.K, kv.V)
		}
	case Members:
		for _, x := range t {
			WriteXML(b, "member", x)
		}
	case Items:
		for _, x := range t {
			WriteXML(b, "item", x)
		}
	case Named:
		for _, x := range t.Values {
			WriteXML(b, t.Name, x)
		}
	case []string:
		for _, x := range t {
			WriteXML(b, "member", x)
		}
	case []any:
		for _, x := range t {
			WriteXML(b, "member", x)
		}
	default:
		writeReflect(b, t)
	}
}

func (q *Req) writeQuery(v any) {
	var b strings.Builder
	b.WriteString(xml.Header)
	ns := q.Svc.XMLNS
	resp := q.Op + "Response"
	fmt.Fprintf(&b, `<%s xmlns="%s">`, resp, ns)
	switch {
	case q.Svc.EC2:
		fmt.Fprintf(&b, "<requestId>%s</requestId>", q.RequestID)
		writeInner(&b, v)
	default:
		if _, none := v.(NoResult); !none && v != nil {
			WriteXML(&b, q.Op+"Result", v)
		}
		fmt.Fprintf(&b, "<ResponseMetadata><RequestId>%s</RequestId></ResponseMetadata>", q.RequestID)
	}
	fmt.Fprintf(&b, "</%s>", resp)
	q.W.Header().Set("Content-Type", "text/xml")
	q.W.WriteHeader(http.StatusOK)
	_, _ = q.W.Write([]byte(b.String()))
}

func (q *Req) writeQueryError(e *Error) {
	var b strings.Builder
	b.WriteString(xml.Header)
	if q.Svc.EC2 {
		b.WriteString("<Response><Errors><Error>")
		WriteXML(&b, "Code", e.Code)
		WriteXML(&b, "Message", e.Message)
		b.WriteString("</Error></Errors>")
		WriteXML(&b, "RequestID", q.RequestID)
		b.WriteString("</Response>")
	} else {
		typ := "Sender"
		if e.Status >= 500 {
			typ = "Receiver"
		}
		code := e.Code
		if e.QueryCode != "" {
			code = e.QueryCode // services that also speak awsJson keep their legacy awsQuery codes
		}
		fmt.Fprintf(&b, `<ErrorResponse xmlns="%s"><Error>`, q.Svc.XMLNS)
		WriteXML(&b, "Type", typ)
		WriteXML(&b, "Code", code)
		WriteXML(&b, "Message", e.Message)
		b.WriteString("</Error>")
		WriteXML(&b, "RequestId", q.RequestID)
		b.WriteString("</ErrorResponse>")
	}
	q.W.Header().Set("Content-Type", "text/xml")
	q.W.WriteHeader(e.Status)
	_, _ = q.W.Write([]byte(b.String()))
}

// WriteXMLDoc writes a restXml response: <root xmlns=ns>children</root>.
func (q *Req) WriteXMLDoc(status int, root, ns string, children any) {
	var b strings.Builder
	b.WriteString(xml.Header)
	if ns != "" {
		fmt.Fprintf(&b, `<%s xmlns="%s">`, root, ns)
	} else {
		fmt.Fprintf(&b, "<%s>", root)
	}
	writeInner(&b, children)
	fmt.Fprintf(&b, "</%s>", root)
	q.W.Header().Set("Content-Type", "application/xml")
	q.W.WriteHeader(status)
	_, _ = q.W.Write([]byte(b.String()))
}

// ---- awsQuery input ----

// Param returns a form parameter.
func (q *Req) Param(name string) string { return q.Form.Get(name) }

// ParamInt returns an integer form parameter, or def.
func (q *Req) ParamInt(name string, def int) int {
	if v, err := strconv.Atoi(q.Form.Get(name)); err == nil {
		return v
	}
	return def
}

// ParamBool returns a boolean form parameter ("true"/"false"), or def.
func (q *Req) ParamBool(name string, def bool) bool {
	switch strings.ToLower(q.Form.Get(name)) {
	case "true":
		return true
	case "false":
		return false
	}
	return def
}

// List returns an indexed list parameter. It accepts the awsQuery form
// ("Name.member.1", "Name.member.2") and the flattened/ec2 form ("Name.1").
func (q *Req) List(name string) []string {
	var out []string
	for _, prefix := range []string{name + ".member.", name + "."} {
		for i := 1; ; i++ {
			v, ok := q.Form[prefix+strconv.Itoa(i)]
			if !ok {
				break
			}
			out = append(out, v[0])
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

// Structs returns indexed structure parameters ("Tags.member.1.Key", "Filter.1.Name")
// as maps of their fields; nested lists stay in dotted form ("Value.1").
func (q *Req) Structs(name string) []map[string]string {
	var out []map[string]string
	for _, prefix := range []string{name + ".member.", name + "."} {
		for i := 1; ; i++ {
			p := prefix + strconv.Itoa(i) + "."
			m := map[string]string{}
			for k, v := range q.Form {
				if strings.HasPrefix(k, p) {
					m[strings.TrimPrefix(k, p)] = v[0]
				}
			}
			if len(m) == 0 {
				break
			}
			out = append(out, m)
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

// Map returns a map parameter in the awsQuery "entry" form
// ("Attributes.entry.1.key" / ".value") or the flattened form ("Attribute.1.Name" / ".Value").
func (q *Req) Map(name, keyField, valueField string) map[string]string {
	out := map[string]string{}
	for _, prefix := range []string{name + ".entry.", name + "."} {
		for i := 1; ; i++ {
			p := prefix + strconv.Itoa(i) + "."
			k, ok := q.Form[p+keyField]
			if !ok {
				break
			}
			out[k[0]] = q.Form.Get(p + valueField)
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}
