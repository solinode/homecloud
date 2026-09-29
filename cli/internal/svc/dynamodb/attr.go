package dynamodb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind is a DynamoDB attribute type.
type Kind uint8

const (
	kInvalid Kind = iota
	kS
	kN
	kB
	kSS
	kNS
	kBS
	kM
	kL
	kNULL
	kBOOL
)

var kindNames = [...]string{"", "S", "N", "B", "SS", "NS", "BS", "M", "L", "NULL", "BOOL"}

func (k Kind) String() string { return kindNames[k] }

func kindOf(name string) Kind {
	for i, n := range kindNames {
		if n == name && i > 0 {
			return Kind(i)
		}
	}
	return kInvalid
}

// AV is a DynamoDB AttributeValue. N and NS hold canonical number strings once
// validated. The zero AV is invalid.
type AV struct {
	Kind Kind
	S    string   // S, N
	B    []byte   // B
	SS   []string // SS, NS
	BS   [][]byte // BS
	M    map[string]AV
	L    []AV
	Bool bool
	bad  string // why the value could not be decoded (reported by validate)
}

// Item is an item: attribute name to value.
type Item = map[string]AV

func Str(s string) AV         { return AV{Kind: kS, S: s} }
func Num(s string) AV         { return AV{Kind: kN, S: s} }
func Bin(b []byte) AV         { return AV{Kind: kB, B: b} }
func Bool(b bool) AV          { return AV{Kind: kBOOL, Bool: b} }
func Null() AV                { return AV{Kind: kNULL} }
func Map(m Item) AV           { return AV{Kind: kM, M: m} }
func List(l []AV) AV          { return AV{Kind: kL, L: l} }
func NumInt(n int64) AV       { return Num(strconv.FormatInt(n, 10)) }
func isSet(k Kind) bool       { return k == kSS || k == kNS || k == kBS }
func isScalarKey(k Kind) bool { return k == kS || k == kN || k == kB }

// ---- JSON (DynamoDB wire format) ----

func (v AV) MarshalJSON() ([]byte, error) {
	return v.appendJSON(nil), nil
}

func appendStr(b []byte, s string) []byte {
	if !needsEscape(s) {
		b = append(b, '"')
		b = append(b, s...)
		return append(b, '"')
	}
	j, _ := json.Marshal(s)
	return append(b, j...)
}

func needsEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' || c >= 0x80 || c == '<' || c == '>' || c == '&' {
			return true
		}
	}
	return false
}

func appendB64(b []byte, data []byte) []byte {
	b = append(b, '"')
	b = base64.StdEncoding.AppendEncode(b, data)
	return append(b, '"')
}

func (v AV) appendJSON(b []byte) []byte {
	b = append(b, `{"`...)
	b = append(b, v.Kind.String()...)
	b = append(b, `":`...)
	switch v.Kind {
	case kS, kN:
		b = appendStr(b, v.S)
	case kB:
		b = appendB64(b, v.B)
	case kSS, kNS:
		b = append(b, '[')
		for i, s := range v.SS {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendStr(b, s)
		}
		b = append(b, ']')
	case kBS:
		b = append(b, '[')
		for i, s := range v.BS {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendB64(b, s)
		}
		b = append(b, ']')
	case kM:
		b = appendItem(b, v.M)
	case kL:
		b = append(b, '[')
		for i, e := range v.L {
			if i > 0 {
				b = append(b, ',')
			}
			b = e.appendJSON(b)
		}
		b = append(b, ']')
	case kNULL:
		b = append(b, "true"...)
	case kBOOL:
		b = strconv.AppendBool(b, v.Bool)
	default:
		b = append(b, "null"...)
	}
	return append(b, '}')
}

func appendItem(b []byte, m Item) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendStr(b, k)
		b = append(b, ':')
		b = m[k].appendJSON(b)
	}
	return append(b, '}')
}

func encodeItem(it Item) []byte { return appendItem(nil, it) }

func decodeItem(b []byte) (Item, error) {
	var it Item
	err := json.Unmarshal(b, &it)
	return it, err
}

func (v *AV) UnmarshalJSON(data []byte) error {
	*v = AV{}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if len(m) == 0 {
		v.bad = "Supplied AttributeValue is empty, must contain exactly one of the supported datatypes"
		return nil
	}
	if len(m) > 1 {
		v.bad = "Supplied AttributeValue has more than one datatypes set, must contain exactly one of the supported datatypes"
		return nil
	}
	for name, raw := range m {
		k := kindOf(name)
		if k == kInvalid {
			v.bad = "Supplied AttributeValue is empty, must contain exactly one of the supported datatypes"
			return nil
		}
		v.Kind = k
		var err error
		switch k {
		case kS, kN:
			err = json.Unmarshal(raw, &v.S)
		case kB:
			err = json.Unmarshal(raw, &v.B)
		case kSS, kNS:
			err = json.Unmarshal(raw, &v.SS)
			if err == nil && v.SS == nil {
				v.SS = []string{}
			}
		case kBS:
			err = json.Unmarshal(raw, &v.BS)
			if err == nil && v.BS == nil {
				v.BS = [][]byte{}
			}
		case kM:
			err = json.Unmarshal(raw, &v.M)
			if err == nil && v.M == nil {
				v.M = Item{}
			}
		case kL:
			err = json.Unmarshal(raw, &v.L)
			if err == nil && v.L == nil {
				v.L = []AV{}
			}
		case kNULL:
			var t bool
			err = json.Unmarshal(raw, &t)
			if err == nil && !t {
				v.bad = "One or more parameter values were invalid: Null attribute value types must have the value of true"
			}
		case kBOOL:
			err = json.Unmarshal(raw, &v.Bool)
		}
		if err != nil {
			return fmt.Errorf("attribute value of type %s: %v", name, err)
		}
	}
	return nil
}

// ---- validation ----

// validate checks a value the way DynamoDB does and canonicalizes numbers.
func (v *AV) validate() error {
	if v.bad != "" {
		return validation("%s", v.bad)
	}
	switch v.Kind {
	case kInvalid:
		return validation("Supplied AttributeValue is empty, must contain exactly one of the supported datatypes")
	case kS:
		if !utf8.ValidString(v.S) {
			return validation("One or more parameter values were invalid: Invalid UTF-8 in string")
		}
	case kN:
		d, err := parseNumber(v.S)
		if err != nil {
			return err
		}
		v.S = d.String()
	case kSS:
		if len(v.SS) == 0 {
			return invalidParam("An string set  may not be empty")
		}
		if err := noDuplicates(v.SS); err != nil {
			return err
		}
	case kNS:
		if len(v.SS) == 0 {
			return invalidParam("An number set  may not be empty")
		}
		out := make([]string, len(v.SS))
		for i, s := range v.SS {
			d, err := parseNumber(s)
			if err != nil {
				return err
			}
			out[i] = d.String()
		}
		if err := noDuplicates(out); err != nil {
			return err
		}
		v.SS = out
	case kBS:
		if len(v.BS) == 0 {
			return invalidParam("Binary sets should not be empty")
		}
		seen := map[string]bool{}
		for _, b := range v.BS {
			if seen[string(b)] {
				return validation("Input collection contains duplicates.")
			}
			seen[string(b)] = true
		}
	case kM:
		for k, e := range v.M {
			if err := e.validate(); err != nil {
				return err
			}
			v.M[k] = e
		}
	case kL:
		for i := range v.L {
			if err := v.L[i].validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func noDuplicates(ss []string) error {
	seen := make(map[string]bool, len(ss))
	for _, s := range ss {
		if seen[s] {
			return validation("Input collection [%s] contains duplicates.", strings.Join(ss, ", "))
		}
		seen[s] = true
	}
	return nil
}

func validateItem(it Item) error {
	for k, v := range it {
		if k == "" {
			return invalidParam("An AttributeValue may not contain an empty string")
		}
		if err := v.validate(); err != nil {
			return err
		}
		it[k] = v
	}
	return nil
}

// ---- equality, ordering, size ----

func equalAV(a, b AV) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case kS, kN:
		return a.S == b.S
	case kB:
		return bytes.Equal(a.B, b.B)
	case kBOOL:
		return a.Bool == b.Bool
	case kNULL:
		return true
	case kSS, kNS:
		if len(a.SS) != len(b.SS) {
			return false
		}
		set := make(map[string]bool, len(a.SS))
		for _, s := range a.SS {
			set[s] = true
		}
		for _, s := range b.SS {
			if !set[s] {
				return false
			}
		}
		return true
	case kBS:
		if len(a.BS) != len(b.BS) {
			return false
		}
		set := make(map[string]bool, len(a.BS))
		for _, s := range a.BS {
			set[string(s)] = true
		}
		for _, s := range b.BS {
			if !set[string(s)] {
				return false
			}
		}
		return true
	case kM:
		return equalItems(a.M, b.M)
	case kL:
		if len(a.L) != len(b.L) {
			return false
		}
		for i := range a.L {
			if !equalAV(a.L[i], b.L[i]) {
				return false
			}
		}
		return true
	}
	return false
}

func equalItems(a, b Item) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !equalAV(v, w) {
			return false
		}
	}
	return true
}

// compareAV orders two scalar values of the same type (S byte-wise, N
// numerically, B byte-wise). ok is false for other types or mixed types.
func compareAV(a, b AV) (int, bool) {
	if a.Kind != b.Kind {
		return 0, false
	}
	switch a.Kind {
	case kS:
		return strings.Compare(a.S, b.S), true
	case kB:
		return bytes.Compare(a.B, b.B), true
	case kN:
		x, err1 := parseDecimal(a.S)
		y, err2 := parseDecimal(b.S)
		if err1 != nil || err2 != nil {
			return 0, false
		}
		return x.cmp(y), true
	}
	return 0, false
}

// avSize approximates DynamoDB's size accounting for a value.
func avSize(v AV) int {
	switch v.Kind {
	case kS:
		return len(v.S)
	case kN:
		return numberSize(v.S)
	case kB:
		return len(v.B)
	case kBOOL, kNULL:
		return 1
	case kSS:
		n := 0
		for _, s := range v.SS {
			n += len(s)
		}
		return n
	case kNS:
		n := 0
		for _, s := range v.SS {
			n += numberSize(s)
		}
		return n
	case kBS:
		n := 0
		for _, b := range v.BS {
			n += len(b)
		}
		return n
	case kM:
		n := 3
		for k, e := range v.M {
			n += len(k) + avSize(e) + 1
		}
		return n
	case kL:
		n := 3
		for _, e := range v.L {
			n += avSize(e) + 1
		}
		return n
	}
	return 0
}

func itemSize(it Item) int {
	n := 0
	for k, v := range it {
		n += len(k) + avSize(v)
	}
	return n
}

// ---- copying ----

func (v AV) clone() AV {
	switch v.Kind {
	case kB:
		v.B = append([]byte(nil), v.B...)
	case kSS, kNS:
		v.SS = append([]string(nil), v.SS...)
	case kBS:
		bs := make([][]byte, len(v.BS))
		for i, b := range v.BS {
			bs[i] = append([]byte(nil), b...)
		}
		v.BS = bs
	case kM:
		v.M = cloneItem(v.M)
	case kL:
		l := make([]AV, len(v.L))
		for i, e := range v.L {
			l[i] = e.clone()
		}
		v.L = l
	}
	return v
}

func cloneItem(it Item) Item {
	if it == nil {
		return nil
	}
	out := make(Item, len(it))
	for k, v := range it {
		out[k] = v.clone()
	}
	return out
}

// ---- plain JSON (native API) ----

// fromPlain converts a plain JSON value (as decoded with UseNumber) to an AV:
// strings are S, numbers N, booleans BOOL, null NULL, arrays L, objects M.
func fromPlain(x any) (AV, error) {
	switch t := x.(type) {
	case nil:
		return Null(), nil
	case string:
		return Str(t), nil
	case bool:
		return Bool(t), nil
	case json.Number:
		d, err := parseNumber(string(t))
		if err != nil {
			return AV{}, err
		}
		return Num(d.String()), nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return AV{}, errNumberFormat
		}
		d, err := parseNumber(strconv.FormatFloat(t, 'g', -1, 64))
		if err != nil {
			return AV{}, err
		}
		return Num(d.String()), nil
	case int:
		return NumInt(int64(t)), nil
	case int64:
		return NumInt(t), nil
	case []any:
		l := make([]AV, len(t))
		for i, e := range t {
			v, err := fromPlain(e)
			if err != nil {
				return AV{}, err
			}
			l[i] = v
		}
		return List(l), nil
	case map[string]any:
		m, err := itemFromPlain(t)
		if err != nil {
			return AV{}, err
		}
		return Map(m), nil
	}
	return AV{}, validation("unsupported value %v", x)
}

func itemFromPlain(m map[string]any) (Item, error) {
	if m == nil {
		return nil, nil
	}
	it := make(Item, len(m))
	for k, e := range m {
		v, err := fromPlain(e)
		if err != nil {
			return nil, err
		}
		it[k] = v
	}
	return it, nil
}

// toPlain converts an AV to plain JSON: numbers become json.Number, binary
// values base64 strings and sets arrays.
func toPlain(v AV) any {
	switch v.Kind {
	case kS:
		return v.S
	case kN:
		return json.Number(v.S)
	case kB:
		return base64.StdEncoding.EncodeToString(v.B)
	case kBOOL:
		return v.Bool
	case kNULL:
		return nil
	case kSS:
		return append([]string{}, v.SS...)
	case kNS:
		out := make([]json.Number, len(v.SS))
		for i, s := range v.SS {
			out[i] = json.Number(s)
		}
		return out
	case kBS:
		out := make([]string, len(v.BS))
		for i, b := range v.BS {
			out[i] = base64.StdEncoding.EncodeToString(b)
		}
		return out
	case kM:
		return itemToPlain(v.M)
	case kL:
		out := make([]any, len(v.L))
		for i, e := range v.L {
			out[i] = toPlain(e)
		}
		return out
	}
	return nil
}

func itemToPlain(it Item) map[string]any {
	if it == nil {
		return nil
	}
	out := make(map[string]any, len(it))
	for k, v := range it {
		out[k] = toPlain(v)
	}
	return out
}

// PlainItem is a native-API item decoded from plain JSON with exact numbers.
type PlainItem map[string]any

func (p *PlainItem) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	*p = m
	return nil
}

// PlainValue is a native-API value decoded with exact numbers.
type PlainValue struct{ V any }

func (p *PlainValue) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(&p.V)
}

// describe renders a value for error messages the way DynamoDB does.
func (v AV) describe() string {
	return "AttributeValue: {" + v.Kind.String() + ":" + v.short() + "}"
}

func (v AV) short() string {
	switch v.Kind {
	case kS, kN:
		return v.S
	case kB:
		return base64.StdEncoding.EncodeToString(v.B)
	case kBOOL:
		return strconv.FormatBool(v.Bool)
	case kNULL:
		return "true"
	}
	return string(v.appendJSON(nil))
}
