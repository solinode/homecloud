package awsapi

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Smithy RPC v2 CBOR (rpcv2Cbor), the protocol current AWS SDKs use for
// CloudWatch: POST /service/<ServiceShape>/operation/<Operation> with the
// header "Smithy-Protocol: rpc-v2-cbor" and a CBOR (RFC 8949) body. HomeCloud
// turns the request body into the equivalent awsJson body (timestamps as
// epoch seconds, blobs as base64), so a service's awsJson operations serve it
// unchanged, and encodes the result back as CBOR (timestamps as tag 1).

const cborProtocolHeader = "Smithy-Protocol"

// cborOp returns the service shape and operation of an rpcv2Cbor request.
func cborOp(r *http.Request) (service, op string, ok bool) {
	if r.Method != http.MethodPost || r.Header.Get(cborProtocolHeader) != "rpc-v2-cbor" {
		return "", "", false
	}
	p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// The path ends in service/<shape>/operation/<op> (it may carry a prefix).
	if i := len(p) - 4; i >= 0 && p[i] == "service" && p[i+2] == "operation" && p[i+1] != "" && p[i+3] != "" {
		return p[i+1], p[i+3], true
	}
	return "", "", false
}

func cborRequest(r *http.Request) bool {
	_, _, ok := cborOp(r)
	return ok
}

// ---- decoding ----

type cborDecoder struct {
	b   []byte
	pos int
}

var errCBORShort = errors.New("cbor: unexpected end of data")

// CBORToJSON converts a CBOR document to JSON.
func CBORToJSON(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	d := &cborDecoder{b: b}
	v, err := d.value(0)
	if err != nil {
		return nil, err
	}
	if d.pos != len(b) {
		return nil, fmt.Errorf("cbor: %d trailing bytes", len(b)-d.pos)
	}
	return json.Marshal(v)
}

func (d *cborDecoder) byte() (byte, error) {
	if d.pos >= len(d.b) {
		return 0, errCBORShort
	}
	c := d.b[d.pos]
	d.pos++
	return c, nil
}

func (d *cborDecoder) take(n uint64) ([]byte, error) {
	if n > uint64(len(d.b)-d.pos) {
		return nil, errCBORShort
	}
	out := d.b[d.pos : d.pos+int(n)]
	d.pos += int(n)
	return out, nil
}

// arg reads the argument of an initial byte; indefinite reports info 31.
func (d *cborDecoder) arg(info byte) (n uint64, indefinite bool, err error) {
	switch {
	case info < 24:
		return uint64(info), false, nil
	case info == 24, info == 25, info == 26, info == 27:
		b, err := d.take(1 << (info - 24))
		if err != nil {
			return 0, false, err
		}
		for _, c := range b {
			n = n<<8 | uint64(c)
		}
		return n, false, nil
	case info == 31:
		return 0, true, nil
	}
	return 0, false, fmt.Errorf("cbor: invalid additional information %d", info)
}

const cborMaxDepth = 64

func (d *cborDecoder) value(depth int) (any, error) {
	if depth > cborMaxDepth {
		return nil, errors.New("cbor: nesting too deep")
	}
	ib, err := d.byte()
	if err != nil {
		return nil, err
	}
	major, info := ib>>5, ib&0x1f
	if major == 7 {
		return d.simple(info)
	}
	n, indef, err := d.arg(info)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		return json.Number(strconv.FormatUint(n, 10)), nil
	case 1:
		if n == math.MaxUint64 {
			return json.Number("-18446744073709551616"), nil
		}
		return json.Number("-" + strconv.FormatUint(n+1, 10)), nil
	case 2, 3:
		var s []byte
		if indef {
			for {
				if d.pos < len(d.b) && d.b[d.pos] == 0xff {
					d.pos++
					break
				}
				c, err := d.byte()
				if err != nil {
					return nil, err
				}
				if c>>5 != major {
					return nil, errors.New("cbor: bad indefinite-length string chunk")
				}
				l, ind, err := d.arg(c & 0x1f)
				if err != nil || ind {
					return nil, errors.New("cbor: bad indefinite-length string chunk")
				}
				p, err := d.take(l)
				if err != nil {
					return nil, err
				}
				s = append(s, p...)
			}
		} else if s, err = d.take(n); err != nil {
			return nil, err
		}
		if major == 2 {
			return base64.StdEncoding.EncodeToString(s), nil
		}
		return string(s), nil
	case 4:
		out := []any{}
		for i := uint64(0); indef || i < n; i++ {
			if indef && d.pos < len(d.b) && d.b[d.pos] == 0xff {
				d.pos++
				break
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 5:
		out := map[string]any{}
		for i := uint64(0); indef || i < n; i++ {
			if indef && d.pos < len(d.b) && d.b[d.pos] == 0xff {
				d.pos++
				break
			}
			k, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, errors.New("cbor: map key is not a string")
			}
			v, err := d.value(depth + 1)
			if err != nil {
				return nil, err
			}
			out[ks] = v
		}
		return out, nil
	case 6:
		// Tag 1 (epoch time) carries a number, which is what awsJson uses;
		// other tags (bignums, ...) are passed through as their content.
		return d.value(depth + 1)
	}
	return nil, fmt.Errorf("cbor: invalid major type %d", major)
}

func (d *cborDecoder) simple(info byte) (any, error) {
	switch info {
	case 20:
		return false, nil
	case 21:
		return true, nil
	case 22, 23:
		return nil, nil
	case 25:
		b, err := d.take(2)
		if err != nil {
			return nil, err
		}
		return jsonFloat(float64(halfToFloat(binary.BigEndian.Uint16(b))))
	case 26:
		b, err := d.take(4)
		if err != nil {
			return nil, err
		}
		return jsonFloat(float64(math.Float32frombits(binary.BigEndian.Uint32(b))))
	case 27:
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		return jsonFloat(math.Float64frombits(binary.BigEndian.Uint64(b)))
	}
	if info < 24 {
		return nil, nil // unassigned simple value
	}
	if info == 24 {
		_, err := d.byte()
		return nil, err
	}
	return nil, fmt.Errorf("cbor: invalid simple value %d", info)
}

// jsonFloat renders a float for JSON; NaN and infinities become the strings
// the AWS JSON protocols use for them.
func jsonFloat(f float64) (any, error) {
	switch {
	case math.IsNaN(f):
		return "NaN", nil
	case math.IsInf(f, 1):
		return "Infinity", nil
	case math.IsInf(f, -1):
		return "-Infinity", nil
	}
	format := byte('f')
	if a := math.Abs(f); a != 0 && (a >= 1e21 || a < 1e-6) {
		format = 'g'
	}
	return json.Number(strconv.FormatFloat(f, format, -1, 64)), nil
}

func halfToFloat(h uint16) float32 {
	sign := uint32(h>>15) << 31
	exp := (h >> 10) & 0x1f
	frac := uint32(h & 0x3ff)
	switch exp {
	case 0:
		f := float32(frac) / 1024 * float32(math.Pow(2, -14))
		if sign != 0 {
			return -f
		}
		return f
	case 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | frac<<13)
	}
	return math.Float32frombits(sign | uint32(exp+112)<<23 | frac<<13)
}

// ---- encoding ----

type cborEncoder struct{ bytes.Buffer }

// CBORMarshal encodes an operation's result: structs by their json field
// names (omitempty honoured), Time and time.Time as tag 1 epoch seconds,
// []byte as byte strings, json.RawMessage and other json.Marshalers through
// their JSON.
func CBORMarshal(v any) ([]byte, error) {
	var e cborEncoder
	if err := e.encode(reflect.ValueOf(v)); err != nil {
		return nil, err
	}
	return e.Bytes(), nil
}

func (e *cborEncoder) head(major byte, n uint64) {
	switch {
	case n < 24:
		e.WriteByte(major<<5 | byte(n))
	case n <= math.MaxUint8:
		e.WriteByte(major<<5 | 24)
		e.WriteByte(byte(n))
	case n <= math.MaxUint16:
		e.WriteByte(major<<5 | 25)
		e.Write(binary.BigEndian.AppendUint16(nil, uint16(n)))
	case n <= math.MaxUint32:
		e.WriteByte(major<<5 | 26)
		e.Write(binary.BigEndian.AppendUint32(nil, uint32(n)))
	default:
		e.WriteByte(major<<5 | 27)
		e.Write(binary.BigEndian.AppendUint64(nil, n))
	}
}

func (e *cborEncoder) int(i int64) {
	if i < 0 {
		e.head(1, uint64(-(i + 1)))
	} else {
		e.head(0, uint64(i))
	}
}

func (e *cborEncoder) float(f float64) {
	e.WriteByte(0xfb)
	e.Write(binary.BigEndian.AppendUint64(nil, math.Float64bits(f)))
}

func (e *cborEncoder) str(s string) {
	e.head(3, uint64(len(s)))
	e.WriteString(s)
}

func (e *cborEncoder) time(t time.Time) {
	e.WriteByte(0xc1) // tag 1: epoch-based date/time
	if t.Nanosecond() == 0 {
		e.int(t.Unix())
		return
	}
	e.float(float64(t.UnixMilli()) / 1000)
}

var (
	typeTime     = reflect.TypeOf(Time{})
	typeStdTime  = reflect.TypeOf(time.Time{})
	typeRaw      = reflect.TypeOf(json.RawMessage(nil))
	typeNumber   = reflect.TypeOf(json.Number(""))
	typeJSONMars = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
)

func (e *cborEncoder) encode(v reflect.Value) error {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			e.WriteByte(0xf6) // null
			return nil
		}
		v = v.Elem()
	}
	switch v.Type() {
	case typeTime:
		e.time(v.Interface().(Time).Time)
		return nil
	case typeStdTime:
		e.time(v.Interface().(time.Time))
		return nil
	case typeNumber:
		n := v.Interface().(json.Number)
		if i, err := n.Int64(); err == nil {
			e.int(i)
		} else if f, err := n.Float64(); err == nil {
			e.float(f)
		} else {
			e.str(string(n))
		}
		return nil
	case typeRaw:
		return e.json(v.Bytes())
	}
	if v.Kind() != reflect.Struct && v.Type().Implements(typeJSONMars) {
		b, err := v.Interface().(json.Marshaler).MarshalJSON()
		if err != nil {
			return err
		}
		return e.json(b)
	}
	switch v.Kind() {
	case reflect.Struct:
		if v.Type().Implements(typeJSONMars) || reflect.PointerTo(v.Type()).Implements(typeJSONMars) {
			b, err := json.Marshal(v.Interface())
			if err != nil {
				return err
			}
			return e.json(b)
		}
		fields := map[string]reflect.Value{}
		var names []string
		structFields(v, fields, &names)
		e.head(5, uint64(len(names)))
		for _, n := range names {
			e.str(n)
			if err := e.encode(fields[n]); err != nil {
				return err
			}
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		e.head(5, uint64(len(keys)))
		for _, k := range keys {
			e.str(fmt.Sprint(k.Interface()))
			if err := e.encode(v.MapIndex(k)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			e.head(2, uint64(v.Len()))
			e.Write(v.Bytes())
			return nil
		}
		e.head(4, uint64(v.Len()))
		for i := 0; i < v.Len(); i++ {
			if err := e.encode(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.String:
		e.str(v.String())
	case reflect.Bool:
		if v.Bool() {
			e.WriteByte(0xf5)
		} else {
			e.WriteByte(0xf4)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		e.head(0, v.Uint())
	case reflect.Float32, reflect.Float64:
		e.float(v.Float())
	default:
		return fmt.Errorf("cbor: cannot encode %s", v.Type())
	}
	return nil
}

// structFields collects a struct's encoded fields by JSON name, flattening
// embedded structs, skipping nil and omitempty-zero fields.
func structFields(v reflect.Value, out map[string]reflect.Value, names *[]string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		fv := v.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && f.Type.Kind() == reflect.Struct && tag == "" {
			structFields(fv, out, names)
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
		if strings.Contains(tag, ",omitempty") && fv.IsZero() {
			continue
		}
		if _, dup := out[name]; !dup {
			*names = append(*names, name)
		}
		out[name] = fv
	}
}

// json re-encodes a JSON document as CBOR (numbers as integers when whole).
func (e *cborEncoder) json(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	return e.encode(reflect.ValueOf(v))
}

// ---- responses ----

func (q *Req) writeCBOR(status int, v any) {
	if v == nil {
		v = struct{}{}
	}
	b, err := CBORMarshal(v)
	if err != nil {
		e := Errorf(http.StatusInternalServerError, "InternalFailure", "encode response: %v", err)
		q.writeCBORError(e)
		return
	}
	q.W.Header().Set(cborProtocolHeader, "rpc-v2-cbor")
	q.W.Header().Set("Content-Type", "application/cbor")
	q.W.WriteHeader(status)
	_, _ = q.W.Write(b)
}

func (q *Req) writeCBORError(e *Error) {
	if e.QueryCode != "" {
		fault := "Sender"
		if e.Status >= 500 {
			fault = "Receiver"
		}
		q.W.Header().Set("x-amzn-query-error", e.QueryCode+";"+fault)
	}
	body := map[string]any{"__type": e.Code, "message": e.Message}
	for k, v := range e.Fields {
		body[k] = v
	}
	b, _ := CBORMarshal(body)
	q.W.Header().Set(cborProtocolHeader, "rpc-v2-cbor")
	q.W.Header().Set("Content-Type", "application/cbor")
	q.W.WriteHeader(e.Status)
	_, _ = q.W.Write(b)
}
