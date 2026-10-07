package awsapi

import (
	"encoding/hex"
	"net/http/httptest"
	"testing"
	"time"
)

// Decoding vectors from RFC 8949 appendix A, as the JSON operations see them.
func TestCBORToJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{"00", "0"}, {"17", "23"}, {"1864", "100"}, {"1a000f4240", "1000000"}, {"1bffffffffffffffff", "18446744073709551615"},
		{"20", "-1"}, {"3863", "-100"}, {"f90000", "0"}, {"f93c00", "1"}, {"fb3ff199999999999a", "1.1"}, {"f97bff", "65504"},
		{"fa47c35000", "100000"}, {"f97c00", `"Infinity"`}, {"f4", "false"}, {"f5", "true"}, {"f6", "null"},
		{"c11a514b67b0", "1363896240"}, {"c1fb41d452d9ec200000", "1363896240.5"},
		{"4401020304", `"AQIDBA=="`}, {"6449455446", `"IETF"`}, {"62225c", `"\"\\"`}, {"80", "[]"}, {"83010203", "[1,2,3]"},
		{"a26161016162820203", `{"a":1,"b":[2,3]}`}, {"5f42010243030405ff", `"AQIDBAU="`}, {"7f657374726561646d696e67ff", `"streaming"`},
		{"9f018202039f0405ffff", "[1,[2,3],[4,5]]"}, {"bf61610161629f0203ffff", `{"a":1,"b":[2,3]}`},
	}
	for _, c := range cases {
		b, _ := hex.DecodeString(c.in)
		got, err := CBORToJSON(b)
		if err != nil || string(got) != c.want {
			t.Errorf("%s: got %s %v, want %s", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"18", "62ff", "a1016161", "1c", "8301", "00ff"} {
		b, _ := hex.DecodeString(bad)
		if _, err := CBORToJSON(b); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

func TestCBORMarshal(t *testing.T) {
	type inner struct {
		N    int     `json:"n"`
		Skip *string `json:",omitempty"`
	}
	v := struct {
		Name  string
		At    *Time
		When  time.Time
		Rate  float64
		Big   uint64
		Neg   int
		Blob  []byte
		List  []inner
		Map   map[string]bool
		Empty string `json:",omitempty"`
		Raw   any
	}{"x", T(time.Unix(1363896240, 0)), time.UnixMilli(1363896240500), 1.5, 1000000, -100, []byte{1, 2},
		[]inner{{N: 1}}, map[string]bool{"b": false, "a": true}, "", map[string]any{"k": []any{"v"}}}
	b, err := CBORMarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want := "aa" + "644e616d65" + "6178" + "624174" + "c11a514b67b0" + "645768656e" + "c1fb41d452d9ec200000" +
		"6452617465" + "fb3ff8000000000000" + "63426967" + "1a000f4240" + "634e6567" + "3863" + "64426c6f62" + "420102" +
		"644c697374" + "81a1616e01" + "634d6170" + "a26161f56162f4" + "63526177" + "a1616b816176"
	if got := hex.EncodeToString(b); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// What the encoder writes decodes back to the awsJson form.
	j, err := CBORToJSON(b)
	if err != nil || string(j) != `{"At":1363896240,"Big":1000000,"Blob":"AQI=","List":[{"n":1}],"Map":{"a":true,"b":false},"Name":"x","Neg":-100,"Rate":1.5,"Raw":{"k":["v"]},"When":1363896240.5}` {
		t.Fatalf("round trip %s %v", j, err)
	}
}

func TestCBOROp(t *testing.T) {
	r := httptest.NewRequest("POST", "/service/GraniteServiceVersion20100801/operation/PutMetricData", nil)
	if _, _, ok := cborOp(r); ok {
		t.Fatal("matched without the Smithy-Protocol header")
	}
	r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	if s, op, ok := cborOp(r); !ok || s != "GraniteServiceVersion20100801" || op != "PutMetricData" {
		t.Fatalf("got %s %s %v", s, op, ok)
	}
	r = httptest.NewRequest("POST", "/service/X/operation/", nil)
	r.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	if _, _, ok := cborOp(r); ok {
		t.Fatal("matched an empty operation")
	}
}
