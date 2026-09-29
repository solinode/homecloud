package sqs

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

// SQS inputs and outputs are Go structs shaped like the awsJson documents
// (json tags). The awsQuery protocol flattens lists and maps and renames some
// members; a `query:"Name[,KeyName,ValueName]"` tag gives the awsQuery name
// (and for maps the entry key/value element names, default Name/Value).
//
//	awsJson:  {"Attributes": {"VisibilityTimeout": "30"}}
//	awsQuery: Attribute.1.Name=VisibilityTimeout&Attribute.1.Value=30
//	          <Attribute><Name>VisibilityTimeout</Name><Value>30</Value></Attribute>

type fieldInfo struct {
	json, query, key, value string
	omitempty               bool
}

func fieldOf(f reflect.StructField) (fieldInfo, bool) {
	jt := f.Tag.Get("json")
	if jt == "-" || !f.IsExported() {
		return fieldInfo{}, false
	}
	name, opts, _ := strings.Cut(jt, ",")
	if name == "" {
		name = f.Name
	}
	fi := fieldInfo{json: name, query: name, key: "Name", value: "Value", omitempty: strings.Contains(opts, "omitempty")}
	if qt := f.Tag.Get("query"); qt != "" {
		parts := strings.Split(qt, ",")
		fi.query = parts[0]
		if len(parts) == 3 {
			fi.key, fi.value = parts[1], parts[2]
		}
	}
	return fi, true
}

func badParam(name, value string) error {
	return &awsapi.Error{Status: http.StatusBadRequest, Code: "InvalidParameterValue", QueryCode: "InvalidParameterValue",
		Message: "Value " + value + " for parameter " + name + " is invalid."}
}

// hasPrefix reports whether any form key starts with p.
func hasPrefix(form url.Values, p string) bool {
	for k := range form {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// decodeForm fills the struct v from awsQuery parameters under prefix.
func decodeForm(form url.Values, prefix string, v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if sf := t.Field(i); sf.Anonymous && sf.Type.Kind() == reflect.Struct {
			if err := decodeForm(form, prefix, v.Field(i)); err != nil {
				return err
			}
			continue
		}
		fi, ok := fieldOf(t.Field(i))
		if !ok {
			continue
		}
		if err := decodeValue(form, prefix+fi.query, fi, v.Field(i)); err != nil {
			return err
		}
	}
	return nil
}

func decodeValue(form url.Values, key string, fi fieldInfo, f reflect.Value) error {
	switch f.Kind() {
	case reflect.Pointer:
		if _, ok := form[key]; !ok && !(f.Type().Elem().Kind() == reflect.Struct && hasPrefix(form, key+".")) {
			return nil
		}
		nv := reflect.New(f.Type().Elem())
		if err := decodeValue(form, key, fi, nv.Elem()); err != nil {
			return err
		}
		f.Set(nv)
	case reflect.String:
		if vs, ok := form[key]; ok {
			f.SetString(vs[0])
		}
	case reflect.Int, reflect.Int64:
		if vs, ok := form[key]; ok {
			n, err := strconv.ParseInt(strings.TrimSpace(vs[0]), 10, 64)
			if err != nil {
				return badParam(key, vs[0])
			}
			f.SetInt(n)
		}
	case reflect.Bool:
		if vs, ok := form[key]; ok {
			b, err := strconv.ParseBool(vs[0])
			if err != nil {
				return badParam(key, vs[0])
			}
			f.SetBool(b)
		}
	case reflect.Struct:
		return decodeForm(form, key+".", f)
	case reflect.Slice:
		if f.Type().Elem().Kind() == reflect.Uint8 { // []byte: base64
			if vs, ok := form[key]; ok {
				b, err := base64.StdEncoding.DecodeString(vs[0])
				if err != nil {
					return badParam(key, "(binary)")
				}
				f.SetBytes(b)
			}
			return nil
		}
		for _, p := range []string{key + ".", key + ".member."} {
			out := reflect.MakeSlice(f.Type(), 0, 0)
			for n := 1; ; n++ {
				item := p + strconv.Itoa(n)
				_, direct := form[item]
				if !direct && !hasPrefix(form, item+".") {
					break
				}
				ev := reflect.New(f.Type().Elem()).Elem()
				if err := decodeValue(form, item, fi, ev); err != nil {
					return err
				}
				out = reflect.Append(out, ev)
			}
			if out.Len() > 0 {
				f.Set(out)
				return nil
			}
		}
	case reflect.Map:
		m := reflect.MakeMap(f.Type())
		for _, p := range []string{key + ".", key + ".entry."} {
			for n := 1; ; n++ {
				item := p + strconv.Itoa(n) + "."
				k, ok := form[item+fi.key]
				if !ok {
					break
				}
				ev := reflect.New(f.Type().Elem()).Elem()
				if err := decodeValue(form, item+fi.value, fieldInfo{key: "Name", value: "Value"}, ev); err != nil {
					return err
				}
				m.SetMapIndex(reflect.ValueOf(k[0]), ev)
			}
			if m.Len() > 0 {
				break
			}
		}
		if m.Len() > 0 {
			f.Set(m)
		}
	}
	return nil
}

// toXML converts an output struct into awsapi XML values.
func toXML(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return toXML(v.Elem())
	case reflect.Struct:
		var out awsapi.Ordered
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			if sf := t.Field(i); sf.Anonymous && sf.Type.Kind() == reflect.Struct {
				if inner, ok := toXML(v.Field(i)).(awsapi.Ordered); ok {
					out = append(out, inner...)
				}
				continue
			}
			fi, ok := fieldOf(t.Field(i))
			if !ok {
				continue
			}
			f := v.Field(i)
			if fi.omitempty && f.IsZero() {
				continue
			}
			if f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.Uint8 {
				out = append(out, awsapi.KV{K: fi.query, V: base64.StdEncoding.EncodeToString(f.Bytes())})
				continue
			}
			switch f.Kind() {
			case reflect.Slice:
				flat := awsapi.Flat{}
				for j := 0; j < f.Len(); j++ {
					flat = append(flat, toXML(f.Index(j)))
				}
				out = append(out, awsapi.KV{K: fi.query, V: flat})
			case reflect.Map:
				keys := make([]string, 0, f.Len())
				for _, k := range f.MapKeys() {
					keys = append(keys, k.String())
				}
				sort.Strings(keys)
				flat := awsapi.Flat{}
				for _, k := range keys {
					flat = append(flat, awsapi.Ordered{{K: fi.key, V: k}, {K: fi.value, V: toXML(f.MapIndex(reflect.ValueOf(k)))}})
				}
				out = append(out, awsapi.KV{K: fi.query, V: flat})
			default:
				out = append(out, awsapi.KV{K: fi.query, V: toXML(f)})
			}
		}
		return out
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return base64.StdEncoding.EncodeToString(v.Bytes())
		}
		return nil
	default:
		return v.Interface()
	}
}
