package dynamodb

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	bolt "go.etcd.io/bbolt"
)

// DynamoDB Streams: every table stream has a single shard whose records are
// kept for 24 hours.

const iteratorTTL = 15 * time.Minute

func shardID(t *Table, st StreamInfo) string {
	h := sha256.Sum256([]byte(t.ID + st.Label))
	return fmt.Sprintf("shardId-%020d-%s", st.Created.UnixMilli(), hex.EncodeToString(h[:4]))
}

// streamByARN resolves a stream ARN to its table and stream.
func (s *Service) streamByARN(arn string) (*Table, *StreamInfo, error) {
	notFound := errf("ResourceNotFoundException", "Requested resource not found: Stream: %s not found", arn)
	i := strings.Index(arn, "/stream/")
	if i < 0 {
		return nil, nil, validation("Invalid StreamArn")
	}
	name, err := s.tableFromARN(arn[:i])
	if err != nil {
		return nil, nil, err
	}
	label := arn[i+len("/stream/"):]
	t, err := s.getTable(name)
	if err != nil {
		return nil, nil, notFound
	}
	for j := range t.Streams {
		if t.Streams[j].Label == label {
			return t, &t.Streams[j], nil
		}
	}
	return nil, nil, notFound
}

// streamBounds returns the lowest retained and the last sequence number.
func (s *Service) streamBounds(t *Table, st *StreamInfo) (first, last uint64) {
	_ = s.db.View(func(tx *bolt.Tx) error {
		tb := tx.Bucket(tableBucketName(t.Name))
		if tb == nil {
			return nil
		}
		last = uint64(counter(tb, "seq:"+st.Label))
		if sb := tb.Bucket(streamBucketName(st.Label)); sb != nil {
			if k, _ := sb.Cursor().First(); k != nil {
				first = binary.BigEndian.Uint64(k)
			}
		}
		return nil
	})
	if first == 0 {
		first = last + 1
	}
	return
}

func (s *Service) streamDesc(t *Table, st *StreamInfo) map[string]any {
	status := "ENABLED"
	if st.Disabled != nil {
		status = "DISABLED"
	}
	_, last := s.streamBounds(t, st)
	rng := map[string]any{"StartingSequenceNumber": formatSeq(1)}
	if st.Disabled != nil {
		rng["EndingSequenceNumber"] = formatSeq(last)
	}
	return map[string]any{
		"StreamArn":               t.streamARN(st.Label),
		"StreamLabel":             st.Label,
		"StreamStatus":            status,
		"StreamViewType":          st.ViewType,
		"CreationRequestDateTime": awsapi.Epoch(st.Created),
		"TableName":               t.Name,
		"KeySchema":               keySchemaOut(t.PartitionKey, t.SortKey),
		"Shards":                  []map[string]any{{"ShardId": shardID(t, *st), "SequenceNumberRange": rng}},
	}
}

func (s *Service) awsListStreams(q *awsapi.Req) (any, error) {
	var in struct {
		TableName               string
		Limit                   *int
		ExclusiveStartStreamArn string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	res := "*"
	if in.TableName != "" {
		res = s.tableARN(in.TableName) + "/stream/*"
	}
	if err := q.Authorize("dynamodb:ListStreams", res); err != nil {
		return nil, err
	}
	limit := 100
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > 100 {
			return nil, validation("1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: Member must have value between 1 and 100", *in.Limit)
		}
		limit = *in.Limit
	}
	var names []string
	if in.TableName != "" {
		if _, err := s.getTable(in.TableName); err != nil {
			return nil, errf("ResourceNotFoundException", "Requested resource not found: Table: %s not found", in.TableName)
		}
		names = []string{in.TableName}
	} else {
		names = s.listTableNames()
	}
	out := []map[string]any{}
	started := in.ExclusiveStartStreamArn == ""
	var lastARN string
	more := false
	for _, n := range names {
		t, err := s.getTable(n)
		if err != nil {
			continue
		}
		for _, st := range t.Streams {
			arn := t.streamARN(st.Label)
			if !started {
				if arn == in.ExclusiveStartStreamArn {
					started = true
				}
				continue
			}
			if len(out) == limit {
				more = true
				break
			}
			out = append(out, map[string]any{"StreamArn": arn, "TableName": t.Name, "StreamLabel": st.Label})
			lastARN = arn
		}
	}
	r := map[string]any{"Streams": out}
	if more {
		r["LastEvaluatedStreamArn"] = lastARN
	}
	return r, nil
}

func (s *Service) awsDescribeStream(q *awsapi.Req) (any, error) {
	var in struct {
		StreamArn             string
		Limit                 *int
		ExclusiveStartShardId string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeStream", in.StreamArn); err != nil {
		return nil, err
	}
	t, st, err := s.streamByARN(in.StreamArn)
	if err != nil {
		return nil, err
	}
	d := s.streamDesc(t, st)
	if in.ExclusiveStartShardId != "" {
		d["Shards"] = []map[string]any{}
	}
	return map[string]any{"StreamDescription": d}, nil
}

type shardIterator struct {
	ARN   string `json:"a"`
	Shard string `json:"s"`
	Pos   uint64 `json:"p"` // next sequence number to return
	At    int64  `json:"t"`
}

func (it shardIterator) encode() string {
	b, _ := json.Marshal(it)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeIterator(s string) (shardIterator, error) {
	var it shardIterator
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &it) != nil || it.ARN == "" {
		return it, validation("Invalid ShardIterator")
	}
	return it, nil
}

func (s *Service) awsGetShardIterator(q *awsapi.Req) (any, error) {
	var in struct {
		StreamArn         string
		ShardId           string
		ShardIteratorType string
		SequenceNumber    string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:GetShardIterator", in.StreamArn); err != nil {
		return nil, err
	}
	t, st, err := s.streamByARN(in.StreamArn)
	if err != nil {
		return nil, err
	}
	if in.ShardId != shardID(t, *st) {
		return nil, errf("ResourceNotFoundException", "Requested resource not found: Shard does not exist")
	}
	_, last := s.streamBounds(t, st)
	it := shardIterator{ARN: in.StreamArn, Shard: in.ShardId, At: s.now().Unix()}
	switch in.ShardIteratorType {
	case "TRIM_HORIZON":
		it.Pos = 0
	case "LATEST":
		it.Pos = last + 1
	case "AT_SEQUENCE_NUMBER", "AFTER_SEQUENCE_NUMBER":
		n, ok := parseSeq(in.SequenceNumber)
		if !ok || n == 0 || n > last {
			return nil, validation("Invalid SequenceNumber for ShardIteratorType %s: %s", in.ShardIteratorType, in.SequenceNumber)
		}
		it.Pos = n
		if in.ShardIteratorType == "AFTER_SEQUENCE_NUMBER" {
			it.Pos++
		}
	default:
		return nil, validation("1 validation error detected: Value '%s' at 'shardIteratorType' failed to satisfy constraint: Member must satisfy enum value set: [AFTER_SEQUENCE_NUMBER, LATEST, AT_SEQUENCE_NUMBER, TRIM_HORIZON]", in.ShardIteratorType)
	}
	return map[string]any{"ShardIterator": it.encode()}, nil
}

// readStream returns up to limit records with sequence numbers >= pos, and the
// position after the last one returned.
func (s *Service) readStream(t *Table, st *StreamInfo, pos uint64, limit int) ([]json.RawMessage, uint64, error) {
	out := []json.RawMessage{}
	next := pos
	err := s.db.View(func(tx *bolt.Tx) error {
		tb := tx.Bucket(tableBucketName(t.Name))
		if tb == nil {
			return errf("ResourceNotFoundException", "Requested resource not found: Stream: %s not found", t.streamARN(st.Label))
		}
		sb := tb.Bucket(streamBucketName(st.Label))
		if sb == nil {
			return nil
		}
		c := sb.Cursor()
		start := make([]byte, 8)
		binary.BigEndian.PutUint64(start, pos)
		size := 0
		for k, v := c.Seek(start); k != nil && len(out) < limit && size < pageBytes; k, v = c.Next() {
			out = append(out, append(json.RawMessage(nil), v...))
			size += len(v)
			next = binary.BigEndian.Uint64(k) + 1
		}
		return nil
	})
	return out, next, err
}

func (s *Service) awsGetRecords(q *awsapi.Req) (any, error) {
	var in struct {
		ShardIterator string
		Limit         *int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	it, err := decodeIterator(in.ShardIterator)
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:GetRecords", it.ARN); err != nil {
		return nil, err
	}
	if s.now().Unix()-it.At > int64(iteratorTTL/time.Second) {
		return nil, errf("ExpiredIteratorException", "Iterator expired. The iterator was created at time %s while right now it is %s. The iterator is only valid for 15 minutes.",
			time.Unix(it.At, 0).UTC().Format(time.RFC1123), s.now().Format(time.RFC1123))
	}
	limit := 1000
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > 1000 {
			return nil, validation("1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: Member must have value between 1 and 1000", *in.Limit)
		}
		limit = *in.Limit
	}
	t, st, err := s.streamByARN(it.ARN)
	if err != nil {
		return nil, err
	}
	recs, next, err := s.readStream(t, st, it.Pos, limit)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"Records": recs}
	_, last := s.streamBounds(t, st)
	if st.Disabled != nil && next > last {
		return res, nil // shard closed and fully read: no next iterator
	}
	nit := shardIterator{ARN: it.ARN, Shard: it.Shard, Pos: next, At: s.now().Unix()}
	res["NextShardIterator"] = nit.encode()
	return res, nil
}

// ---- in-process consumers (Lambda event source mappings) ----

// LatestStreamARN returns the ARN of a table's enabled stream.
func (s *Service) LatestStreamARN(table string) (string, bool) {
	t, err := s.getTable(table)
	if err != nil {
		return "", false
	}
	if st := t.stream(); st != nil {
		return t.streamARN(st.Label), true
	}
	return "", false
}

// ReadStream returns up to limit records after sequence number `after` ("" to
// start at the trim horizon, "LATEST" to skip existing records) and the
// sequence number to resume after. A Lambda event source poller keeps the
// returned checkpoint and delivers {"Records": [...]} with eventSourceARN set
// to the stream ARN.
func (s *Service) ReadStream(streamARN, after string, limit int) ([]StreamRecord, string, error) {
	t, st, err := s.streamByARN(streamARN)
	if err != nil {
		return nil, after, err
	}
	var pos uint64
	switch after {
	case "":
	case "LATEST":
		_, last := s.streamBounds(t, st)
		return nil, formatSeq(last), nil
	default:
		n, ok := parseSeq(after)
		if !ok {
			return nil, after, validation("invalid sequence number %q", after)
		}
		pos = n + 1
	}
	raw, next, err := s.readStream(t, st, pos, limit)
	if err != nil {
		return nil, after, err
	}
	recs := make([]StreamRecord, 0, len(raw))
	for _, r := range raw {
		var rec StreamRecord
		if err := json.Unmarshal(r, &rec); err != nil {
			return nil, after, err
		}
		recs = append(recs, rec)
	}
	if len(recs) == 0 {
		return recs, after, nil
	}
	return recs, formatSeq(next - 1), nil
}
