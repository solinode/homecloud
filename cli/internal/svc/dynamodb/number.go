package dynamodb

import (
	"encoding/binary"
	"math/big"
	"strconv"
	"strings"
)

// decimal is a DynamoDB number: an arbitrary-precision decimal with up to 38
// significant digits. Its value is 0.<digits> × 10^exp; digits has no leading
// or trailing zeros, and is empty for zero.
type decimal struct {
	neg    bool
	digits string
	exp    int
}

const (
	maxDigits = 38
	maxExp    = 126  // 9.99…E+125
	minExp    = -129 // 1E-130
)

var (
	errNumberFormat    = validation("A value provided cannot be converted into a number")
	errNumberPrecision = validation("Attempting to store more than 38 significant digits in a Number")
	errNumberOverflow  = validation("Number overflow. Attempting to store a number with magnitude larger than supported range")
	errNumberUnderflow = validation("Number underflow. Attempting to store a number with magnitude smaller than supported range")
)

// parseNumber parses and range-checks a DynamoDB number string.
func parseNumber(s string) (decimal, error) {
	d, err := parseDecimal(s)
	if err != nil {
		return d, err
	}
	return d, d.check()
}

func parseDecimal(s string) (decimal, error) {
	var d decimal
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		d.neg = s[i] == '-'
		i++
	}
	var intPart, frac strings.Builder
	sawDigit, sawPoint := false, false
	for ; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			sawDigit = true
			if sawPoint {
				frac.WriteByte(c)
			} else {
				intPart.WriteByte(c)
			}
			continue
		case c == '.' && !sawPoint:
			sawPoint = true
			continue
		}
		break
	}
	if !sawDigit {
		return d, errNumberFormat
	}
	exp10 := 0
	if i < len(s) {
		if s[i] != 'e' && s[i] != 'E' {
			return d, errNumberFormat
		}
		es := s[i+1:]
		if es == "" || es == "+" || es == "-" {
			return d, errNumberFormat
		}
		for j, c := range es {
			if !(c >= '0' && c <= '9') && !(j == 0 && (c == '+' || c == '-')) {
				return d, errNumberFormat
			}
		}
		n, err := strconv.Atoi(es)
		if err != nil || n > 100000 || n < -100000 {
			// Absurd exponents: only zero survives.
			if strings.Trim(intPart.String()+frac.String(), "0") == "" {
				return decimal{}, nil
			}
			if strings.HasPrefix(es, "-") {
				return d, errNumberUnderflow
			}
			return d, errNumberOverflow
		}
		exp10 = n
	}
	all := intPart.String() + frac.String()
	point := intPart.Len()
	trimmed := strings.TrimLeft(all, "0")
	point -= len(all) - len(trimmed)
	trimmed = strings.TrimRight(trimmed, "0")
	if trimmed == "" {
		return decimal{}, nil
	}
	d.digits = trimmed
	d.exp = point + exp10
	return d, nil
}

func (d decimal) check() error {
	if d.digits == "" {
		return nil
	}
	if len(d.digits) > maxDigits {
		return errNumberPrecision
	}
	if d.exp > maxExp {
		return errNumberOverflow
	}
	if d.exp < minExp {
		return errNumberUnderflow
	}
	return nil
}

func (d decimal) isZero() bool { return d.digits == "" }

// String is the canonical form DynamoDB returns: plain decimal notation
// without leading or trailing zeros.
func (d decimal) String() string {
	if d.digits == "" {
		return "0"
	}
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	switch {
	case d.exp <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -d.exp))
		b.WriteString(d.digits)
	case d.exp >= len(d.digits):
		b.WriteString(d.digits)
		b.WriteString(strings.Repeat("0", d.exp-len(d.digits)))
	default:
		b.WriteString(d.digits[:d.exp])
		b.WriteByte('.')
		b.WriteString(d.digits[d.exp:])
	}
	return b.String()
}

func cmpMagnitude(a, b decimal) int {
	switch {
	case a.digits == "" && b.digits == "":
		return 0
	case a.digits == "":
		return -1
	case b.digits == "":
		return 1
	case a.exp != b.exp:
		if a.exp < b.exp {
			return -1
		}
		return 1
	}
	return strings.Compare(a.digits, b.digits)
}

// cmp compares two decimals numerically.
func (d decimal) cmp(o decimal) int {
	ds, os := d.sign(), o.sign()
	if ds != os {
		if ds < os {
			return -1
		}
		return 1
	}
	c := cmpMagnitude(d, o)
	if ds < 0 {
		return -c
	}
	return c
}

func (d decimal) sign() int {
	switch {
	case d.digits == "":
		return 0
	case d.neg:
		return -1
	}
	return 1
}

// rat converts d to an integer mantissa and a power-of-ten scale.
func (d decimal) scaled() (*big.Int, int) {
	if d.digits == "" {
		return new(big.Int), 0
	}
	m, _ := new(big.Int).SetString(d.digits, 10)
	if d.neg {
		m.Neg(m)
	}
	return m, d.exp - len(d.digits)
}

var ten = big.NewInt(10)

// add returns d+o, range-checked.
func (d decimal) add(o decimal) (decimal, error) {
	am, as := d.scaled()
	bm, bs := o.scaled()
	if d.digits == "" {
		as = bs
	}
	if o.digits == "" {
		bs = as
	}
	scale := as
	if bs < scale {
		scale = bs
	}
	am.Mul(am, new(big.Int).Exp(ten, big.NewInt(int64(as-scale)), nil))
	bm.Mul(bm, new(big.Int).Exp(ten, big.NewInt(int64(bs-scale)), nil))
	am.Add(am, bm)
	var r decimal
	if am.Sign() == 0 {
		return r, nil
	}
	r.neg = am.Sign() < 0
	str := new(big.Int).Abs(am).String()
	r.exp = len(str) + scale
	r.digits = strings.TrimRight(str, "0")
	return r, r.check()
}

func (d decimal) negate() decimal {
	if d.digits != "" {
		d.neg = !d.neg
	}
	return d
}

// sortBytes encodes d so that byte order matches numeric order.
func (d decimal) sortBytes() []byte {
	if d.digits == "" {
		return []byte{0x02}
	}
	b := make([]byte, 3, 4+len(d.digits))
	e := uint16(d.exp + 0x8000)
	if !d.neg {
		b[0] = 0x03
		binary.BigEndian.PutUint16(b[1:], e)
		return append(b, d.digits...)
	}
	b[0] = 0x01
	binary.BigEndian.PutUint16(b[1:], ^e)
	for i := 0; i < len(d.digits); i++ {
		b = append(b, '0'+('9'-d.digits[i]))
	}
	return append(b, ':') // sorts after every digit, so -0.12 > -0.123
}

// numberSize is the storage size DynamoDB charges for a number.
func numberSize(canonical string) int {
	d, err := parseDecimal(canonical)
	if err != nil {
		return len(canonical)
	}
	return (len(d.digits)+1)/2 + 1
}
