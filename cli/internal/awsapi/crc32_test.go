package awsapi

import (
	"hash/crc32"
	"net/http/httptest"
	"strconv"
	"testing"
)

// awsJson responses carry X-Amz-Crc32 of the body, as DynamoDB's do: the Go v2
// SDK validates DynamoDB bodies against it (and warns on every call without it).
func TestJSONResponsesCarryCRC32(t *testing.T) {
	for name, write := range map[string]func(q *Req){
		"result": func(q *Req) { q.writeJSON(200, map[string]any{"Item": map[string]any{"id": map[string]string{"S": "1"}}}) },
		"empty":  func(q *Req) { q.writeJSON(200, nil) },
		"error":  func(q *Req) { q.writeJSONError(&Error{Status: 400, Code: "ResourceNotFoundException", Message: "no table"}) },
	} {
		w := httptest.NewRecorder()
		write(&Req{W: w, Svc: &Service{Name: "dynamodb", JSONVersion: "1.0"}})
		want := strconv.FormatUint(uint64(crc32.ChecksumIEEE(w.Body.Bytes())), 10)
		if got := w.Header().Get("X-Amz-Crc32"); got != want || w.Body.Len() == 0 {
			t.Errorf("%s: X-Amz-Crc32 %q, body CRC %s (%q)", name, got, want, w.Body)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/x-amz-json-1.0" {
			t.Errorf("%s: Content-Type %q", name, ct)
		}
	}
}
