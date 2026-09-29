package s3

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

// aws-chunked request bodies (x-amz-content-sha256 STREAMING-*):
//
//	STREAMING-AWS4-HMAC-SHA256-PAYLOAD          signed chunks
//	STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER  signed chunks and a signed checksum trailer
//	STREAMING-UNSIGNED-PAYLOAD-TRAILER          plain chunks and a checksum trailer
//
// The proxy decodes them (verifying chunk signatures with the caller's key) and
// sends MinIO either the plain body or, when there is a checksum trailer, an
// unsigned aws-chunked body with the same trailer, so MinIO verifies and stores
// the checksum.

const (
	streamingSigned        = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingSignedTrailer = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	streamingUnsignedTrl   = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	emptySHA256            = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	maxChunk               = 16 << 20 // signed chunks are buffered to verify them first
	maxTrailer             = 16 << 10
	reencodeChunk          = 64 << 10
)

var errChunk = awsapi.Errorf(http.StatusBadRequest, "IncompleteBody", "malformed aws-chunked request body")

// trailerLen is the encoded length of each supported checksum trailer value.
var trailerLen = map[string]int{
	"x-amz-checksum-crc32":     8,
	"x-amz-checksum-crc32c":    8,
	"x-amz-checksum-crc64nvme": 12,
	"x-amz-checksum-sha1":      28,
	"x-amz-checksum-sha256":    44,
}

// chunkSigner verifies the chunk signature chain of a signed streaming upload.
type chunkSigner struct {
	key      []byte
	date     string // X-Amz-Date
	scope    string
	prevSig  string
	emptyHex string
}

func newChunkSigner(sig *awsapi.Signature, secret string) *chunkSigner {
	return &chunkSigner{key: sig.SigningKey(secret), date: sig.AmzDate.Format("20060102T150405Z"), scope: sig.Scope(), prevSig: sig.Signature}
}

func (c *chunkSigner) check(algo, dataHash, got string) bool {
	var sts string
	if algo == "AWS4-HMAC-SHA256-TRAILER" {
		sts = strings.Join([]string{algo, c.date, c.scope, c.prevSig, dataHash}, "\n")
	} else {
		sts = strings.Join([]string{algo, c.date, c.scope, c.prevSig, emptySHA256, dataHash}, "\n")
	}
	m := hmac.New(sha256.New, c.key)
	m.Write([]byte(sts))
	want := hex.EncodeToString(m.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(got))) {
		return false
	}
	c.prevSig = want
	return true
}

// chunkedReader decodes an aws-chunked body into the raw object bytes.
type chunkedReader struct {
	r        *bufio.Reader
	signer   *chunkSigner // nil for unsigned chunks
	trailer  bool         // a trailer section follows the final chunk
	decoded  int64        // declared x-amz-decoded-content-length
	total    int64
	buf      []byte // current chunk (signed: verified before it is released)
	left     int64  // unsigned: bytes of the current chunk still to read
	done     bool
	err      error
	trailers http.Header
}

func newChunkedReader(body io.Reader, signer *chunkSigner, trailer bool, decoded int64) *chunkedReader {
	return &chunkedReader{r: bufio.NewReaderSize(body, 64<<10), signer: signer, trailer: trailer, decoded: decoded, trailers: http.Header{}}
}

func (c *chunkedReader) fail(err error) (int, error) {
	c.err = err
	return 0, err
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	for len(c.buf) == 0 && c.left == 0 {
		if c.done {
			return 0, io.EOF
		}
		if err := c.next(); err != nil {
			return c.fail(err)
		}
	}
	if len(c.buf) > 0 {
		n := copy(p, c.buf)
		c.buf = c.buf[n:]
		return n, nil
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if err == io.EOF && c.left > 0 {
		return c.fail(io.ErrUnexpectedEOF)
	}
	if c.left == 0 && n > 0 {
		if err := c.crlf(); err != nil {
			c.err = err
			return n, nil // the error surfaces on the next Read
		}
	}
	if err != nil && err != io.EOF {
		return c.fail(err)
	}
	return n, nil
}

func (c *chunkedReader) crlf() error {
	b := make([]byte, 2)
	if _, err := io.ReadFull(c.r, b); err != nil || string(b) != "\r\n" {
		return errChunk
	}
	return nil
}

func (c *chunkedReader) line(limit int) (string, error) {
	var b []byte
	for {
		frag, err := c.r.ReadSlice('\n')
		b = append(b, frag...)
		if len(b) > limit {
			return "", errChunk
		}
		if err == nil {
			return strings.TrimRight(string(b), "\r\n"), nil
		}
		if err != bufio.ErrBufferFull {
			if err == io.EOF && len(b) > 0 {
				return strings.TrimRight(string(b), "\r\n"), nil
			}
			return "", errChunk
		}
	}
}

// next reads the next chunk header (and, for signed chunks, the verified data).
func (c *chunkedReader) next() error {
	hdr, err := c.line(4096)
	if err != nil {
		return err
	}
	sizeStr, ext, _ := strings.Cut(hdr, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeStr), 16, 64)
	if err != nil || size < 0 {
		return errChunk
	}
	sig := ""
	for _, e := range strings.Split(ext, ";") {
		if k, v, ok := strings.Cut(strings.TrimSpace(e), "="); ok && k == "chunk-signature" {
			sig = v
		}
	}
	c.total += size
	if c.total > c.decoded {
		return awsapi.Errorf(http.StatusBadRequest, "IncompleteBody", "the body is longer than x-amz-decoded-content-length")
	}
	if c.signer != nil {
		if size > maxChunk {
			return awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "aws-chunked chunks may not exceed %d bytes", maxChunk)
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(c.r, data); err != nil {
			return errChunk
		}
		h := sha256.Sum256(data)
		if sig == "" || !c.signer.check("AWS4-HMAC-SHA256-PAYLOAD", hex.EncodeToString(h[:]), sig) {
			return awsapi.Errorf(http.StatusForbidden, "SignatureDoesNotMatch", "the chunk signature does not match")
		}
		if size > 0 {
			if err := c.crlf(); err != nil {
				return err
			}
			c.buf = data
			return nil
		}
	} else if size > 0 {
		c.left = size
		return nil
	}
	// Final chunk.
	if c.total != c.decoded {
		return awsapi.Errorf(http.StatusBadRequest, "IncompleteBody", "the body is shorter than x-amz-decoded-content-length")
	}
	c.done = true
	return c.readTrailer()
}

// readTrailer parses what follows the final chunk: "name:value" lines (and
// x-amz-trailer-signature for signed trailers), ending with an empty line.
func (c *chunkedReader) readTrailer() error {
	rest, err := io.ReadAll(io.LimitReader(c.r, maxTrailer+1))
	if err != nil || len(rest) > maxTrailer {
		return errChunk
	}
	var signed bytes.Buffer
	trailerSig := ""
	for _, ln := range strings.Split(string(rest), "\n") {
		ln = strings.TrimRight(ln, "\r")
		if ln == "" {
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			return errChunk
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "x-amz-trailer-signature" {
			trailerSig = v
			continue
		}
		c.trailers.Set(k, v)
		signed.WriteString(k + ":" + v + "\n")
	}
	if !c.trailer {
		if len(c.trailers) > 0 {
			return errChunk
		}
		if c.signer != nil && trailerSig != "" {
			return errChunk
		}
		return nil
	}
	if c.signer != nil {
		h := sha256.Sum256(signed.Bytes())
		if trailerSig == "" || !c.signer.check("AWS4-HMAC-SHA256-TRAILER", hex.EncodeToString(h[:]), trailerSig) {
			return awsapi.Errorf(http.StatusForbidden, "SignatureDoesNotMatch", "the trailer signature does not match")
		}
	}
	return nil
}

// unsignedTrailerLength is the Content-Length of reencode's output.
func unsignedTrailerLength(n int64, names []string) (int64, error) {
	chunk := func(size int64) int64 { return int64(len(strconv.FormatInt(size, 16))) + 2 + size + 2 }
	total := (n / reencodeChunk) * chunk(reencodeChunk)
	if r := n % reencodeChunk; r > 0 {
		total += chunk(r)
	}
	total += 3 // "0\r\n"
	for _, name := range names {
		l, ok := trailerLen[name]
		if !ok {
			return 0, awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "unsupported trailer %q", name)
		}
		total += int64(len(name) + 1 + l + 2)
	}
	return total + 2, nil
}

// reencoder re-frames decoded data as an unsigned aws-chunked body with the
// decoded trailers (STREAMING-UNSIGNED-PAYLOAD-TRAILER).
type reencoder struct {
	src   *chunkedReader
	names []string
	out   bytes.Buffer
	chunk []byte
	eof   bool
}

func newReencoder(src *chunkedReader, names []string) *reencoder {
	return &reencoder{src: src, names: names, chunk: make([]byte, reencodeChunk)}
}

func (e *reencoder) Read(p []byte) (int, error) {
	for e.out.Len() == 0 {
		if e.eof {
			return 0, io.EOF
		}
		n, err := io.ReadFull(e.src, e.chunk)
		if n > 0 {
			fmt.Fprintf(&e.out, "%x\r\n", n)
			e.out.Write(e.chunk[:n])
			e.out.WriteString("\r\n")
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			if e.src.err != nil {
				return 0, e.src.err
			}
			e.eof = true
			e.out.WriteString("0\r\n")
			for _, name := range e.names {
				v := e.src.trailers.Get(name)
				if len(v) != trailerLen[name] {
					return 0, awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "missing or malformed trailer %s", name)
				}
				e.out.WriteString(name + ":" + v + "\r\n")
			}
			e.out.WriteString("\r\n")
		} else if err != nil {
			return 0, err
		}
	}
	return e.out.Read(p)
}

// trailerNames parses the X-Amz-Trailer header.
func trailerNames(h http.Header) []string {
	var out []string
	for _, v := range h.Values("X-Amz-Trailer") {
		for _, n := range strings.Split(v, ",") {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}
