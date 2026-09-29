package awsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Time is a timestamp in an AWS API shape. It encodes as epoch seconds in
// awsJson and as ISO 8601 in XML, and decodes from either form, so one Go
// struct serves services that speak both awsJson and awsQuery (CloudWatch).
type Time struct{ time.Time }

// T returns t as a *Time, or nil when t is zero (the field is then omitted).
func T(t time.Time) *Time {
	if t.IsZero() {
		return nil
	}
	return &Time{t}
}

func (t Time) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', -1, 64)), nil
}

func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		return nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		t.Time = epochTime(f)
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return fmt.Errorf("invalid timestamp %s", s)
	}
	v, err := ParseTime(str)
	if err != nil {
		return err
	}
	t.Time = v
	return nil
}

func epochTime(f float64) time.Time {
	sec := int64(f)
	return time.Unix(sec, int64((f-float64(sec))*1e9)).UTC().Round(time.Millisecond)
}

// ParseTime parses the timestamp forms AWS clients send: ISO 8601 (with or
// without fractional seconds or a zone) or epoch seconds.
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return epochTime(f), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}

// Check is Authorize for secondary permissions (e.g. the targets a rule
// delivers to): it does not change what CloudTrail records for the request.
func (q *Req) Check(action, resource string) error {
	a, r := q.action, q.resource
	err := q.Authorize(action, resource)
	q.action, q.resource = a, r
	return err
}

// Decode reads the request input into v: the JSON body for awsJson, the form
// parameters for awsQuery (following the awsQuery serialization rules for
// structures, lists ("Name.member.N") and maps ("Name.entry.N.key")).
func (q *Req) Decode(v any) error {
	if q.Protocol != Query {
		return q.Bind(v)
	}
	if err := DecodeForm(q.Form, v); err != nil {
		return Errorf(http.StatusBadRequest, "InvalidParameterValue", "%v", err)
	}
	return nil
}

// DecodeForm decodes awsQuery form parameters into the struct pointed to by v.
// Field names are the Go field names (or their json tag names).
func DecodeForm(form url.Values, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("DecodeForm needs a pointer")
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	d := formDecoder{form: form, keys: keys}
	_, err := d.decode("", rv.Elem())
	return err
}

type formDecoder struct {
	form url.Values
	keys []string // sorted
}

// has reports whether any parameter is p itself or starts with p + ".".
func (d *formDecoder) has(p string) bool {
	if _, ok := d.form[p]; ok {
		return true
	}
	i := sort.SearchStrings(d.keys, p+".")
	return i < len(d.keys) && strings.HasPrefix(d.keys[i], p+".")
}

var timeType = reflect.TypeOf(Time{})

func fieldName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	return name, true
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// decode fills v from parameters under prefix and reports whether any were present.
func (d *formDecoder) decode(prefix string, v reflect.Value) (bool, error) {
	t := v.Type()
	switch {
	case t == timeType:
		s, ok := d.form[prefix]
		if !ok {
			return false, nil
		}
		tm, err := ParseTime(s[0])
		if err != nil {
			return true, fmt.Errorf("%s: %v", prefix, err)
		}
		v.Set(reflect.ValueOf(Time{tm}))
		return true, nil
	case t.Kind() == reflect.Pointer:
		if prefix != "" && !d.has(prefix) {
			return false, nil
		}
		n := reflect.New(t.Elem())
		ok, err := d.decode(prefix, n.Elem())
		if ok {
			v.Set(n)
		}
		return ok, err
	case t.Kind() == reflect.Struct:
		any := false
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				ok, err := d.decode(prefix, v.Field(i))
				if err != nil {
					return true, err
				}
				any = any || ok
				continue
			}
			name, ok := fieldName(f)
			if !ok {
				continue
			}
			found, err := d.decode(join(prefix, name), v.Field(i))
			if err != nil {
				return true, err
			}
			any = any || found
		}
		return any, nil
	case t.Kind() == reflect.Slice:
		// An empty list is sent as "Name=".
		if s, ok := d.form[prefix]; ok && len(s) > 0 && s[0] == "" {
			v.Set(reflect.MakeSlice(t, 0, 0))
			return true, nil
		}
		for _, sep := range []string{".member.", "."} {
			out := reflect.MakeSlice(t, 0, 0)
			for i := 1; ; i++ {
				p := prefix + sep + strconv.Itoa(i)
				if !d.has(p) {
					break
				}
				e := reflect.New(t.Elem()).Elem()
				if _, err := d.decode(p, e); err != nil {
					return true, err
				}
				out = reflect.Append(out, e)
			}
			if out.Len() > 0 {
				v.Set(out)
				return true, nil
			}
		}
		return false, nil
	case t.Kind() == reflect.Map:
		for _, sep := range []string{".entry.", "."} {
			out := reflect.MakeMap(t)
			for i := 1; ; i++ {
				p := prefix + sep + strconv.Itoa(i)
				if !d.has(p) {
					break
				}
				k := reflect.New(t.Key()).Elem()
				e := reflect.New(t.Elem()).Elem()
				kn, vn := "key", "value"
				if sep == "." {
					kn, vn = "Name", "Value"
				}
				if _, err := d.decode(p+"."+kn, k); err != nil {
					return true, err
				}
				if _, err := d.decode(p+"."+vn, e); err != nil {
					return true, err
				}
				out.SetMapIndex(k, e)
			}
			if out.Len() > 0 {
				v.Set(out)
				return true, nil
			}
		}
		return false, nil
	}
	s, ok := d.form[prefix]
	if !ok {
		return false, nil
	}
	raw := s[0]
	switch t.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return true, fmt.Errorf("%s must be true or false", prefix)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return true, fmt.Errorf("%s must be an integer", prefix)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return true, fmt.Errorf("%s must be a non-negative integer", prefix)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return true, fmt.Errorf("%s must be a number", prefix)
		}
		v.SetFloat(f)
	case reflect.Interface:
		v.Set(reflect.ValueOf(raw))
	default:
		return true, fmt.Errorf("%s: unsupported parameter type %s", prefix, t)
	}
	return true, nil
}

// writeReflect renders structs, slices and named scalar types for awsQuery
// responses: struct fields become elements (json tag names, omitempty
// honoured), slices become <member> lists.
func writeReflect(b *strings.Builder, v any) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			fv := rv.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				writeReflect(b, fv.Interface())
				continue
			}
			name, ok := fieldName(f)
			if !ok {
				continue
			}
			switch fv.Kind() {
			case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map:
				if fv.IsNil() {
					continue
				}
			}
			if strings.Contains(f.Tag.Get("json"), ",omitempty") && fv.IsZero() {
				continue
			}
			WriteXML(b, name, fv.Interface())
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			WriteXML(b, "member", rv.Index(i).Interface())
		}
	case reflect.Map:
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		for _, k := range keys {
			b.WriteString("<entry>")
			WriteXML(b, "key", k.Interface())
			WriteXML(b, "value", rv.MapIndex(k).Interface())
			b.WriteString("</entry>")
		}
	case reflect.String:
		writeInner(b, rv.String())
	case reflect.Bool:
		writeInner(b, rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		writeInner(b, rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		writeInner(b, rv.Uint())
	case reflect.Float32, reflect.Float64:
		writeInner(b, rv.Float())
	default:
		writeInner(b, fmt.Sprint(v))
	}
}

// ---- in-process calls ----

type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// Call invokes an operation of a registered awsJson service in-process as
// principal p, e.g. for Step Functions service integrations. The operation
// authorizes p exactly as it would an SDK caller. It returns the JSON result.
func Call(ctx context.Context, p *httpx.Principal, account, service, op string, input any) (json.RawMessage, error) {
	s := lookup(service)
	if s == nil || s.Ops[op] == nil || s.JSONPrefix == "" {
		return nil, Errorf(http.StatusBadRequest, "UnknownOperationException", "HomeCloud does not implement %s.%s over awsJson", service, op)
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("X-Amz-Target", s.JSONPrefix+"."+op)
	q := &Req{W: &discardWriter{h: http.Header{}}, R: r, P: p, Svc: s, Protocol: JSON, Op: op, Region: core.Region,
		Account: account, RequestID: RequestID(), Body: body, status: 200}
	out, err := s.Ops[op](q)
	if err != nil {
		return nil, toError(s, err)
	}
	if out == nil {
		return json.RawMessage("{}"), nil
	}
	return json.Marshal(out)
}
