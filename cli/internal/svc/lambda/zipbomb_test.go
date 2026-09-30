package lambda

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

type zeros int64

func (z *zeros) Read(p []byte) (int, error) {
	if *z <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > int64(*z) {
		n = int64(*z)
	}
	clear(p[:n])
	*z -= zeros(n)
	return int(n), nil
}

// A few kilobytes of zip can expand to gigabytes; the package is unpacked in
// memory at every cold start, so the expansion must be refused at upload.
func TestCheckZipRefusesDecompressionBombs(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("big.bin")
	z := zeros(maxUnzipped + 1<<20)
	if _, err := io.Copy(w, &z); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	if buf.Len() > maxCodeBytes {
		t.Fatalf("test archive is %d bytes", buf.Len())
	}
	if err := checkZip(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "Unzipped size") {
		t.Fatalf("a bomb of %d bytes (zipped %d) was accepted: %v", maxUnzipped+1<<20, buf.Len(), err)
	}
	if _, err := unzip(buf.Bytes()); err == nil {
		t.Fatal("unzip expanded a bomb")
	}

	many := new(bytes.Buffer)
	zw = zip.NewWriter(many)
	for i := 0; i <= maxZipEntries; i++ {
		zw.Create(string(rune('a'+i%26)) + strings.Repeat("x", i%7) + "/" + strings.Repeat("y", i/26%50) + string(rune(0x4e00+i)))
	}
	zw.Close()
	if err := checkZip(many.Bytes()); err == nil {
		t.Fatal("an archive with too many entries was accepted")
	}

	var ok bytes.Buffer
	zw = zip.NewWriter(&ok)
	w, _ = zw.Create("index.py")
	w.Write([]byte("def handler(e, c): return 1"))
	zw.Close()
	if err := checkZip(ok.Bytes()); err != nil {
		t.Fatal(err)
	}
}
