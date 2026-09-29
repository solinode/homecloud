package awsapi

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDecodeForm(t *testing.T) {
	type dim struct{ Name, Value string }
	var in struct {
		Namespace  string
		MetricData []struct {
			MetricName string
			Dimensions []dim
			Value      *float64
			Values     []float64
			Timestamp  *Time
			Unit       string
			Stats      *struct{ Sum, SampleCount float64 } `json:"StatisticValues"`
		}
		Attrs   map[string]string
		Enabled bool
		Empty   []string
		Count   int
	}
	form, _ := url.ParseQuery("Namespace=App&MetricData.member.1.MetricName=lat&MetricData.member.1.Value=1.5" +
		"&MetricData.member.1.Dimensions.member.1.Name=svc&MetricData.member.1.Dimensions.member.1.Value=api" +
		"&MetricData.member.1.Timestamp=2026-01-02T03:04:05Z&MetricData.member.2.MetricName=n&MetricData.member.2.Values.member.1=1" +
		"&MetricData.member.2.Values.member.2=2&MetricData.member.2.StatisticValues.Sum=3&MetricData.member.2.StatisticValues.SampleCount=2" +
		"&Attrs.entry.1.key=a&Attrs.entry.1.value=b&Enabled=true&Empty=&Count=7")
	if err := DecodeForm(form, &in); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(in)
	want := `{"Namespace":"App","MetricData":[{"MetricName":"lat","Dimensions":[{"Name":"svc","Value":"api"}],"Value":1.5,"Values":null,"Timestamp":1767323045,"Unit":"","StatisticValues":null},` +
		`{"MetricName":"n","Dimensions":null,"Value":null,"Values":[1,2],"Timestamp":null,"Unit":"","StatisticValues":{"Sum":3,"SampleCount":2}}],"Attrs":{"a":"b"},"Enabled":true,"Empty":[],"Count":7}`
	if string(b) != want {
		t.Fatalf("decoded\n%s\nwant\n%s", b, want)
	}
	bad, _ := url.ParseQuery("Count=x")
	if err := DecodeForm(bad, &in); err == nil {
		t.Fatal("bad integer accepted")
	}
}

func TestTimeAndReflectXML(t *testing.T) {
	var v struct{ A, B, C Time }
	if err := json.Unmarshal([]byte(`{"A":1767323045.5,"B":"2026-01-02T03:04:05Z","C":"2026-01-02T03:04:05.250+01:00"}`), &v); err != nil {
		t.Fatal(err)
	}
	if !v.A.Equal(time.Date(2026, 1, 2, 3, 4, 5, 5e8, time.UTC)) || !v.B.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) ||
		!v.C.Equal(time.Date(2026, 1, 2, 2, 4, 5, 25e7, time.UTC)) {
		t.Fatalf("times %+v", v)
	}
	if b, _ := json.Marshal(v.A); string(b) != "1767323045.5" {
		t.Fatalf("epoch %s", b)
	}
	type item struct {
		Name  string
		When  *Time
		Tags  []string
		Extra string  `json:",omitempty"`
		Score float64 `json:"score"`
	}
	var sb strings.Builder
	WriteXML(&sb, "Result", struct {
		Items []item
		Map   map[string]float64
		Next  *string
	}{Items: []item{{Name: "a<b", When: T(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), Tags: []string{"x"}, Score: 1.5}}, Map: map[string]float64{"p99": 2}})
	want := `<Result><Items><member><Name>a&lt;b</Name><When>2026-01-02T03:04:05.000Z</When><Tags><member>x</member></Tags><score>1.5</score></member></Items>` +
		`<Map><entry><key>p99</key><value>2</value></entry></Map></Result>`
	if sb.String() != want {
		t.Fatalf("xml\n%s\nwant\n%s", sb.String(), want)
	}
}
