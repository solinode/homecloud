package dynamodb

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	bolt "go.etcd.io/bbolt"
)

// RegisterAWS serves DynamoDB and DynamoDB Streams over the AWS JSON protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]func(q *awsapi.Req) (any, error){
		"CreateTable":                         s.awsCreateTable,
		"DescribeTable":                       s.awsDescribeTable,
		"ListTables":                          s.awsListTables,
		"DeleteTable":                         s.awsDeleteTable,
		"UpdateTable":                         s.awsUpdateTable,
		"DescribeTimeToLive":                  s.awsDescribeTTL,
		"UpdateTimeToLive":                    s.awsUpdateTTL,
		"DescribeContinuousBackups":           s.awsDescribeBackups,
		"UpdateContinuousBackups":             s.awsUpdateBackups,
		"TagResource":                         s.awsTagResource,
		"UntagResource":                       s.awsUntagResource,
		"ListTagsOfResource":                  s.awsListTags,
		"DescribeLimits":                      s.awsDescribeLimits,
		"DescribeEndpoints":                   s.awsDescribeEndpoints,
		"PutItem":                             s.awsPutItem,
		"GetItem":                             s.awsGetItem,
		"UpdateItem":                          s.awsUpdateItem,
		"DeleteItem":                          s.awsDeleteItem,
		"Query":                               s.awsQuery,
		"Scan":                                s.awsScan,
		"BatchGetItem":                        s.awsBatchGet,
		"BatchWriteItem":                      s.awsBatchWrite,
		"TransactWriteItems":                  s.awsTransactWrite,
		"TransactGetItems":                    s.awsTransactGet,
		"DescribeKinesisStreamingDestination": s.awsDescribeKinesis,
		"DescribeContributorInsights":         s.awsDescribeContributorInsights,
		"ExecuteStatement":                    s.awsExecuteStatement,
		"BatchExecuteStatement":               s.awsBatchExecuteStatement,
		"ExecuteTransaction":                  s.awsExecuteTransaction,
	}
	awsapi.Register(&awsapi.Service{Name: "dynamodb", JSONPrefix: "DynamoDB_20120810", JSONVersion: "1.0", Ops: wrapOps(ops)})
	streams := map[string]func(q *awsapi.Req) (any, error){
		"ListStreams":      s.awsListStreams,
		"DescribeStream":   s.awsDescribeStream,
		"GetShardIterator": s.awsGetShardIterator,
		"GetRecords":       s.awsGetRecords,
	}
	awsapi.Register(&awsapi.Service{Name: "streams.dynamodb", JSONPrefix: "DynamoDBStreams_20120810", JSONVersion: "1.0", Ops: wrapOps(streams)})
}

func wrapOps(ops map[string]func(q *awsapi.Req) (any, error)) map[string]awsapi.Op {
	out := map[string]awsapi.Op{}
	for name, fn := range ops {
		fn := fn
		out[name] = func(q *awsapi.Req) (any, error) {
			v, err := fn(q)
			if err != nil {
				return nil, toAWSError(err)
			}
			return v, nil
		}
	}
	return out
}

func toAWSError(err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		return &awsapi.Error{Status: ae.Status, Code: ae.Code, Message: ae.Message, Fields: ae.Fields}
	}
	return err
}

func (s *Service) tableARN(name string) string { return s.env.ARN("dynamodb", "table/"+name) }

// ---- table descriptions ----

type keySchemaElement struct {
	AttributeName string
	KeyType       string
}

type attrDef struct {
	AttributeName string
	AttributeType string
}

type throughput struct {
	ReadCapacityUnits  int64
	WriteCapacityUnits int64
}

type projectionIn struct {
	ProjectionType   string
	NonKeyAttributes []string
}

func keySchemaOut(pk KeyDef, sk *KeyDef) []map[string]string {
	out := []map[string]string{{"AttributeName": pk.Name, "KeyType": "HASH"}}
	if sk != nil {
		out = append(out, map[string]string{"AttributeName": sk.Name, "KeyType": "RANGE"})
	}
	return out
}

func projectionOut(p Projection) map[string]any {
	m := map[string]any{"ProjectionType": p.Type}
	if len(p.NonKeyAttributes) > 0 {
		m["NonKeyAttributes"] = p.NonKeyAttributes
	}
	return m
}

func throughputOut(r, w int64) map[string]any {
	return map[string]any{"NumberOfDecreasesToday": 0, "ReadCapacityUnits": r, "WriteCapacityUnits": w}
}

func (s *Service) describe(t *Table) map[string]any {
	count, size := s.stats(t)
	attrs := []map[string]string{}
	for _, a := range t.Attributes {
		attrs = append(attrs, map[string]string{"AttributeName": a.Name, "AttributeType": a.Type})
	}
	d := map[string]any{
		"TableName":                 t.Name,
		"TableArn":                  t.ARN,
		"TableId":                   t.ID,
		"TableStatus":               t.Status,
		"KeySchema":                 keySchemaOut(t.PartitionKey, t.SortKey),
		"AttributeDefinitions":      attrs,
		"CreationDateTime":          awsapi.Epoch(t.CreatedAt),
		"ItemCount":                 count,
		"TableSizeBytes":            size,
		"ProvisionedThroughput":     throughputOut(t.ReadCapacity, t.WriteCapacity),
		"TableClassSummary":         map[string]any{"TableClass": t.TableClass},
		"DeletionProtectionEnabled": t.DeletionProtection,
	}
	if t.TableClass == "" {
		d["TableClassSummary"] = map[string]any{"TableClass": "STANDARD"}
	}
	if t.BillingMode == "PAY_PER_REQUEST" {
		bs := map[string]any{"BillingMode": "PAY_PER_REQUEST"}
		ts := t.CreatedAt
		if t.BillingUpdatedAt != nil {
			ts = *t.BillingUpdatedAt
		}
		bs["LastUpdateToPayPerRequestDateTime"] = awsapi.Epoch(ts)
		d["BillingModeSummary"] = bs
	} else {
		d["BillingModeSummary"] = map[string]any{"BillingMode": "PROVISIONED"}
	}
	if len(t.Indexes) > 0 {
		var gsis []map[string]any
		for _, ix := range t.Indexes {
			n, sz := s.indexStats(t, ix.Name)
			g := map[string]any{
				"IndexName": ix.Name, "IndexArn": t.ARN + "/index/" + ix.Name,
				"KeySchema": keySchemaOut(ix.PartitionKey, ix.SortKey), "Projection": projectionOut(ix.projection()),
				"IndexStatus": ix.Status, "ItemCount": n, "IndexSizeBytes": sz,
				"ProvisionedThroughput": throughputOut(ix.ReadCapacity, ix.WriteCapacity),
			}
			if ix.Status == "" {
				g["IndexStatus"] = "ACTIVE"
			}
			gsis = append(gsis, g)
		}
		d["GlobalSecondaryIndexes"] = gsis
	}
	if len(t.LocalIndexes) > 0 {
		var lsis []map[string]any
		for _, ix := range t.LocalIndexes {
			n, sz := s.indexStats(t, ix.Name)
			lsis = append(lsis, map[string]any{
				"IndexName": ix.Name, "IndexArn": t.ARN + "/index/" + ix.Name,
				"KeySchema": keySchemaOut(ix.PartitionKey, ix.SortKey), "Projection": projectionOut(ix.projection()),
				"ItemCount": n, "IndexSizeBytes": sz,
			})
		}
		d["LocalSecondaryIndexes"] = lsis
	}
	if st := t.stream(); st != nil {
		d["StreamSpecification"] = map[string]any{"StreamEnabled": true, "StreamViewType": st.ViewType}
	}
	if n := len(t.Streams); n > 0 {
		d["LatestStreamLabel"] = t.Streams[n-1].Label
		d["LatestStreamArn"] = t.streamARN(t.Streams[n-1].Label)
	}
	if t.SSEEnabled {
		d["SSEDescription"] = map[string]any{"Status": "ENABLED", "SSEType": "KMS", "KMSMasterKeyArn": t.SSEKMSKey}
	}
	return d
}

// ---- table operations ----

type tableIn struct {
	TableName                 string
	AttributeDefinitions      []attrDef
	KeySchema                 []keySchemaElement
	LocalSecondaryIndexes     []indexIn
	GlobalSecondaryIndexes    []indexIn
	BillingMode               string
	ProvisionedThroughput     *throughput
	StreamSpecification       *streamSpec
	SSESpecification          *sseSpec
	Tags                      []tagIn
	TableClass                string
	DeletionProtectionEnabled *bool
}

type indexIn struct {
	IndexName             string
	KeySchema             []keySchemaElement
	Projection            *projectionIn
	ProvisionedThroughput *throughput
}

type streamSpec struct {
	StreamEnabled  bool
	StreamViewType string
}

type sseSpec struct {
	Enabled        *bool
	SSEType        string
	KMSMasterKeyId string
}

type tagIn struct{ Key, Value string }

func parseKeySchema(ks []keySchemaElement, defs map[string]string) (KeyDef, *KeyDef, error) {
	if len(ks) == 0 {
		return KeyDef{}, nil, validation("1 validation error detected: Value null at 'keySchema' failed to satisfy constraint: Member must not be null")
	}
	if len(ks) > 2 {
		return KeyDef{}, nil, validation("1 validation error detected: Value at 'keySchema' failed to satisfy constraint: Member must have length less than or equal to 2")
	}
	if ks[0].KeyType != "HASH" {
		return KeyDef{}, nil, invalidParam("Invalid KeySchema: The first KeySchemaElement is not a HASH key type")
	}
	pk := KeyDef{Name: ks[0].AttributeName, Type: defs[ks[0].AttributeName]}
	if len(ks) == 1 {
		return pk, nil, nil
	}
	if ks[1].KeyType != "RANGE" {
		return KeyDef{}, nil, invalidParam("Invalid KeySchema: The second KeySchemaElement is not a RANGE key type")
	}
	return pk, &KeyDef{Name: ks[1].AttributeName, Type: defs[ks[1].AttributeName]}, nil
}

func (in *indexIn) toIndex(defs map[string]string) (Index, error) {
	pk, sk, err := parseKeySchema(in.KeySchema, defs)
	if err != nil {
		return Index{}, err
	}
	ix := Index{Name: in.IndexName, PartitionKey: pk, SortKey: sk}
	if in.Projection == nil {
		return Index{}, validation("1 validation error detected: Value null at 'projection' failed to satisfy constraint: Member must not be null (index %s)", in.IndexName)
	}
	ix.Projection = &Projection{Type: in.Projection.ProjectionType, NonKeyAttributes: in.Projection.NonKeyAttributes}
	if in.ProvisionedThroughput != nil {
		ix.ReadCapacity, ix.WriteCapacity = in.ProvisionedThroughput.ReadCapacityUnits, in.ProvisionedThroughput.WriteCapacityUnits
	}
	return ix, nil
}

func validStreamSpec(sp *streamSpec) error {
	if sp == nil || !sp.StreamEnabled {
		return nil
	}
	switch sp.StreamViewType {
	case "NEW_IMAGE", "OLD_IMAGE", "NEW_AND_OLD_IMAGES", "KEYS_ONLY":
		return nil
	}
	return invalidParam("StreamViewType must be one of NEW_IMAGE, OLD_IMAGE, NEW_AND_OLD_IMAGES or KEYS_ONLY when StreamEnabled is true")
}

func checkThroughput(mode string, tp *throughput, what string) error {
	if mode == "PAY_PER_REQUEST" {
		if tp != nil && (tp.ReadCapacityUnits != 0 || tp.WriteCapacityUnits != 0) {
			return invalidParam("Neither ReadCapacityUnits nor WriteCapacityUnits can be specified when BillingMode is PAY_PER_REQUEST")
		}
		return nil
	}
	if tp == nil {
		if what == "" {
			return invalidParam("No provisioned throughput specified for the table")
		}
		return invalidParam("ProvisionedThroughput must be specified for index: %s", what)
	}
	if tp.ReadCapacityUnits < 1 || tp.WriteCapacityUnits < 1 {
		return validation("One or more parameter values were invalid: Provisioned throughput values must be at least 1")
	}
	return nil
}

func (s *Service) awsCreateTable(q *awsapi.Req) (any, error) {
	var in tableIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:CreateTable", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	defs := map[string]string{}
	t := &Table{Name: in.TableName}
	for _, a := range in.AttributeDefinitions {
		defs[a.AttributeName] = a.AttributeType
		t.Attributes = append(t.Attributes, KeyDef{Name: a.AttributeName, Type: a.AttributeType})
	}
	if len(in.AttributeDefinitions) == 0 {
		return nil, validation("1 validation error detected: Value null at 'attributeDefinitions' failed to satisfy constraint: Member must not be null")
	}
	pk, sk, err := parseKeySchema(in.KeySchema, defs)
	if err != nil {
		return nil, err
	}
	for _, k := range append([]*KeyDef{&pk}, sk) {
		if k != nil && k.Type == "" {
			return nil, invalidParam("Some index key attributes are not defined in AttributeDefinitions. Keys: [%s], AttributeDefinitions: %s", k.Name, attrNames(t.Attributes))
		}
	}
	t.PartitionKey, t.SortKey = pk, sk
	mode := in.BillingMode
	if mode == "" {
		mode = "PROVISIONED"
	}
	if mode != "PROVISIONED" && mode != "PAY_PER_REQUEST" {
		return nil, validation("1 validation error detected: Value '%s' at 'billingMode' failed to satisfy constraint: Member must satisfy enum value set: [PROVISIONED, PAY_PER_REQUEST]", mode)
	}
	t.BillingMode = mode
	if err := checkThroughput(mode, in.ProvisionedThroughput, ""); err != nil {
		return nil, err
	}
	if in.ProvisionedThroughput != nil {
		t.ReadCapacity, t.WriteCapacity = in.ProvisionedThroughput.ReadCapacityUnits, in.ProvisionedThroughput.WriteCapacityUnits
	}
	for i := range in.GlobalSecondaryIndexes {
		gi := &in.GlobalSecondaryIndexes[i]
		ix, err := gi.toIndex(defs)
		if err != nil {
			return nil, err
		}
		if err := checkThroughput(mode, gi.ProvisionedThroughput, gi.IndexName); err != nil {
			return nil, err
		}
		if err := missingDef(ix, t.Attributes); err != nil {
			return nil, err
		}
		t.Indexes = append(t.Indexes, ix)
	}
	for i := range in.LocalSecondaryIndexes {
		ix, err := in.LocalSecondaryIndexes[i].toIndex(defs)
		if err != nil {
			return nil, err
		}
		if err := missingDef(ix, t.Attributes); err != nil {
			return nil, err
		}
		t.LocalIndexes = append(t.LocalIndexes, ix)
	}
	if err := validStreamSpec(in.StreamSpecification); err != nil {
		return nil, err
	}
	if sp := in.StreamSpecification; sp != nil && sp.StreamEnabled {
		t.Streams = []StreamInfo{{ViewType: sp.StreamViewType}}
	}
	if in.SSESpecification != nil && in.SSESpecification.Enabled != nil && *in.SSESpecification.Enabled {
		t.SSEEnabled = true
		t.SSEKMSKey = in.SSESpecification.KMSMasterKeyId
	}
	if in.TableClass != "" {
		if in.TableClass != "STANDARD" && in.TableClass != "STANDARD_INFREQUENT_ACCESS" {
			return nil, validation("1 validation error detected: Value '%s' at 'tableClass' failed to satisfy constraint: Member must satisfy enum value set: [STANDARD, STANDARD_INFREQUENT_ACCESS]", in.TableClass)
		}
		t.TableClass = in.TableClass
	}
	if in.DeletionProtectionEnabled != nil {
		t.DeletionProtection = *in.DeletionProtectionEnabled
	}
	if len(in.Tags) > 0 {
		t.Tags = core.Tags{}
		for _, tg := range in.Tags {
			t.Tags[tg.Key] = tg.Value
		}
	}
	if err := s.createTable(t); err != nil {
		return nil, err
	}
	d := s.describe(t)
	return map[string]any{"TableDescription": d}, nil
}

func missingDef(ix Index, defs []KeyDef) error {
	for _, k := range []*KeyDef{&ix.PartitionKey, ix.SortKey} {
		if k != nil && k.Type == "" {
			return invalidParam("Some index key attributes are not defined in AttributeDefinitions. Keys: [%s], AttributeDefinitions: %s", k.Name, attrNames(defs))
		}
	}
	return nil
}

type tableNameIn struct{ TableName string }

func (s *Service) awsDescribeTable(q *awsapi.Req) (any, error) {
	var in tableNameIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeTable", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	t, err := s.getTable(in.TableName)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Table": s.describe(t)}, nil
}

func (s *Service) awsListTables(q *awsapi.Req) (any, error) {
	var in struct {
		ExclusiveStartTableName string
		Limit                   *int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:ListTables", s.tableARN("*")); err != nil {
		return nil, err
	}
	limit := 100
	if in.Limit != nil {
		if *in.Limit < 1 || *in.Limit > 100 {
			return nil, validation("1 validation error detected: Value '%d' at 'limit' failed to satisfy constraint: Member must have value between 1 and 100", *in.Limit)
		}
		limit = *in.Limit
	}
	names := s.listTableNames()
	sort.Strings(names)
	out := []string{}
	for _, n := range names {
		if in.ExclusiveStartTableName != "" && n <= in.ExclusiveStartTableName {
			continue
		}
		out = append(out, n)
	}
	res := map[string]any{}
	if len(out) > limit {
		out = out[:limit]
		res["LastEvaluatedTableName"] = out[limit-1]
	}
	res["TableNames"] = out
	return res, nil
}

func (s *Service) awsDeleteTable(q *awsapi.Req) (any, error) {
	var in tableNameIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DeleteTable", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	t, count, size, err := s.deleteTable(in.TableName)
	if err != nil {
		return nil, err
	}
	d := s.describe(t)
	d["ItemCount"], d["TableSizeBytes"], d["TableStatus"] = count, size, "DELETING"
	return map[string]any{"TableDescription": d}, nil
}

func (s *Service) awsUpdateTable(q *awsapi.Req) (any, error) {
	var in struct {
		TableName                   string
		AttributeDefinitions        []attrDef
		BillingMode                 string
		ProvisionedThroughput       *throughput
		GlobalSecondaryIndexUpdates []struct {
			Create *indexIn
			Delete *struct{ IndexName string }
			Update *struct {
				IndexName             string
				ProvisionedThroughput *throughput
			}
		}
		StreamSpecification       *streamSpec
		SSESpecification          *sseSpec
		TableClass                string
		DeletionProtectionEnabled *bool
		ReplicaUpdates            []any
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:UpdateTable", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if len(in.ReplicaUpdates) > 0 {
		return nil, validation("HomeCloud does not support global table replicas")
	}
	if in.BillingMode == "" && in.ProvisionedThroughput == nil && len(in.GlobalSecondaryIndexUpdates) == 0 && in.StreamSpecification == nil &&
		in.SSESpecification == nil && in.TableClass == "" && in.DeletionProtectionEnabled == nil {
		return nil, validation("At least one of ProvisionedThroughput, BillingMode, UpdateStreamEnabled, GlobalSecondaryIndexUpdates or SSESpecification or ReplicaUpdates is required")
	}
	if err := validStreamSpec(in.StreamSpecification); err != nil {
		return nil, err
	}
	t, err := s.alterTable(in.TableName, func(t *Table, tx *bolt.Tx) error {
		mode := t.BillingMode
		if in.BillingMode != "" {
			if in.BillingMode != "PROVISIONED" && in.BillingMode != "PAY_PER_REQUEST" {
				return validation("1 validation error detected: Value '%s' at 'billingMode' failed to satisfy constraint: Member must satisfy enum value set: [PROVISIONED, PAY_PER_REQUEST]", in.BillingMode)
			}
			mode = in.BillingMode
		}
		if in.ProvisionedThroughput != nil || mode != t.BillingMode {
			if err := checkThroughput(mode, in.ProvisionedThroughput, ""); err != nil {
				return err
			}
		}
		if mode != t.BillingMode {
			now := s.now()
			t.BillingMode, t.BillingUpdatedAt = mode, &now
			if mode == "PAY_PER_REQUEST" {
				t.ReadCapacity, t.WriteCapacity = 0, 0
				for i := range t.Indexes {
					t.Indexes[i].ReadCapacity, t.Indexes[i].WriteCapacity = 0, 0
				}
			}
		}
		if in.ProvisionedThroughput != nil {
			t.ReadCapacity, t.WriteCapacity = in.ProvisionedThroughput.ReadCapacityUnits, in.ProvisionedThroughput.WriteCapacityUnits
		}
		defs := map[string]string{}
		for _, a := range t.Attributes {
			defs[a.Name] = a.Type
		}
		for _, a := range in.AttributeDefinitions {
			if d, ok := defs[a.AttributeName]; ok && d != a.AttributeType {
				return invalidParam("Attribute %s is defined with conflicting types", a.AttributeName)
			}
			defs[a.AttributeName] = a.AttributeType
		}
		for _, u := range in.GlobalSecondaryIndexUpdates {
			switch {
			case u.Create != nil:
				ix, err := u.Create.toIndex(defs)
				if err != nil {
					return err
				}
				if err := missingDef(ix, t.Attributes); err != nil {
					return err
				}
				if err := checkThroughput(t.BillingMode, u.Create.ProvisionedThroughput, ix.Name); err != nil {
					return err
				}
				if err := s.addIndex(t, tx, ix); err != nil {
					return err
				}
			case u.Delete != nil:
				if err := s.removeIndex(t, tx, u.Delete.IndexName); err != nil {
					return err
				}
			case u.Update != nil:
				ix, local := t.index(u.Update.IndexName)
				if ix == nil || local {
					return errf("ResourceNotFoundException", "Requested resource not found: Index: %s not found", u.Update.IndexName)
				}
				if u.Update.ProvisionedThroughput != nil {
					ix.ReadCapacity, ix.WriteCapacity = u.Update.ProvisionedThroughput.ReadCapacityUnits, u.Update.ProvisionedThroughput.WriteCapacityUnits
				}
			}
		}
		if sp := in.StreamSpecification; sp != nil {
			if err := s.setStream(t, tx, sp.StreamEnabled, sp.StreamViewType); err != nil {
				return err
			}
		}
		if sp := in.SSESpecification; sp != nil && sp.Enabled != nil {
			t.SSEEnabled, t.SSEKMSKey = *sp.Enabled, sp.KMSMasterKeyId
		}
		if in.TableClass != "" {
			t.TableClass = in.TableClass
		}
		if in.DeletionProtectionEnabled != nil {
			t.DeletionProtection = *in.DeletionProtectionEnabled
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"TableDescription": s.describe(t)}, nil
}

// addIndex adds a GSI and backfills it from the table's items.
func (s *Service) addIndex(t *Table, tx *bolt.Tx, ix Index) error {
	if cur, _ := t.index(ix.Name); cur != nil {
		return validation("One or more parameter values were invalid: Index with name %s already exists", ix.Name)
	}
	cp := *t
	cp.Indexes = append(append([]Index{}, t.Indexes...), ix)
	cp.Attributes = append([]KeyDef{}, t.Attributes...)
	if err := cp.validateSchema(); err != nil {
		return err
	}
	*t = cp
	added, _ := t.index(ix.Name)
	tb, err := tableBucket(tx, t)
	if err != nil {
		return err
	}
	if tb.Bucket(idxBucketName(ix.Name)) != nil {
		if err := tb.DeleteBucket(idxBucketName(ix.Name)); err != nil {
			return err
		}
	}
	ib, err := tb.CreateBucket(idxBucketName(ix.Name))
	if err != nil {
		return err
	}
	name := "idx/" + ix.Name
	m := tb.Bucket(bMeta)
	_ = m.Delete([]byte("n:" + name))
	_ = m.Delete([]byte("s:" + name))
	var n, size int64
	err = tb.Bucket(bItems).ForEach(func(k, v []byte) error {
		it, err := decodeItem(v)
		if err != nil {
			return nil
		}
		if checkIndexKeys(&Table{Indexes: []Index{*added}}, it) != nil {
			return nil // index key violations are skipped, as in DynamoDB's backfill
		}
		ik, ok := indexKeyOf(added.schema(), k, it)
		if !ok {
			return nil
		}
		n++
		size += int64(itemSize(t.projectFor(added, it)))
		return ib.Put(ik, append([]byte(nil), k...))
	})
	if err != nil {
		return err
	}
	_ = bump(tb, "n:"+name, n)
	_ = bump(tb, "s:"+name, size)
	added.Status = "ACTIVE"
	return nil
}

func (s *Service) removeIndex(t *Table, tx *bolt.Tx, name string) error {
	found := false
	for i, ix := range t.Indexes {
		if ix.Name == name {
			t.Indexes = append(t.Indexes[:i:i], t.Indexes[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return errf("ResourceNotFoundException", "Requested resource not found: Index: %s not found", name)
	}
	// Drop attribute definitions no key uses any more.
	cp := *t
	cp.Attributes = nil
	if err := cp.validateSchema(); err == nil {
		t.Attributes = cp.Attributes
	}
	if tb := tx.Bucket(tableBucketName(t.Name)); tb != nil {
		if tb.Bucket(idxBucketName(name)) != nil {
			if err := tb.DeleteBucket(idxBucketName(name)); err != nil {
				return err
			}
		}
		if m := tb.Bucket(bMeta); m != nil {
			_ = m.Delete([]byte("n:idx/" + name))
			_ = m.Delete([]byte("s:idx/" + name))
		}
	}
	return nil
}

// setStream enables or disables the table's stream.
func (s *Service) setStream(t *Table, tx *bolt.Tx, enabled bool, viewType string) error {
	cur := t.stream()
	if enabled {
		if cur != nil {
			return validation("Table already has an enabled stream: %s", t.streamARN(cur.Label))
		}
		now := s.now()
		label := streamLabel(now)
		for _, st := range t.Streams {
			if st.Label == label {
				now = now.Add(time.Millisecond)
				label = streamLabel(now)
			}
		}
		t.Streams = append(t.Streams, StreamInfo{Label: label, ViewType: viewType, Created: now})
		tb, err := tableBucket(tx, t)
		if err != nil {
			return err
		}
		_, err = tb.CreateBucketIfNotExists(streamBucketName(label))
		return err
	}
	if cur == nil {
		return validation("Table does not have a stream to disable")
	}
	now := s.now()
	cur.Disabled = &now
	return nil
}

func (s *Service) awsDescribeTTL(q *awsapi.Req) (any, error) {
	var in tableNameIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeTimeToLive", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	t, err := s.getTable(in.TableName)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"TimeToLiveStatus": "DISABLED"}
	if t.TTLAttribute != "" {
		d = map[string]any{"TimeToLiveStatus": "ENABLED", "AttributeName": t.TTLAttribute}
	}
	return map[string]any{"TimeToLiveDescription": d}, nil
}

func (s *Service) awsUpdateTTL(q *awsapi.Req) (any, error) {
	var in struct {
		TableName               string
		TimeToLiveSpecification struct {
			Enabled       bool
			AttributeName string
		}
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:UpdateTimeToLive", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	spec := in.TimeToLiveSpecification
	if spec.AttributeName == "" {
		return nil, validation("1 validation error detected: Value null at 'timeToLiveSpecification.attributeName' failed to satisfy constraint: Member must not be null")
	}
	_, err := s.alterTable(in.TableName, func(t *Table, _ *bolt.Tx) error {
		if spec.Enabled {
			if t.TTLAttribute != "" {
				return validation("TimeToLive is already enabled")
			}
			t.TTLAttribute = spec.AttributeName
			return nil
		}
		if t.TTLAttribute == "" {
			return validation("TimeToLive is already disabled")
		}
		if t.TTLAttribute != spec.AttributeName {
			return validation("TimeToLive attribute name %s does not match the enabled attribute %s", spec.AttributeName, t.TTLAttribute)
		}
		t.TTLAttribute = ""
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"TimeToLiveSpecification": map[string]any{"Enabled": spec.Enabled, "AttributeName": spec.AttributeName}}, nil
}

func (s *Service) backupsDesc(t *Table) map[string]any {
	pitr := map[string]any{"PointInTimeRecoveryStatus": "DISABLED"}
	if t.PITR {
		now := s.now()
		pitr = map[string]any{"PointInTimeRecoveryStatus": "ENABLED", "RecoveryPeriodInDays": 35,
			"EarliestRestorableDateTime": awsapi.Epoch(t.CreatedAt), "LatestRestorableDateTime": awsapi.Epoch(now.Add(-5 * time.Minute))}
	}
	return map[string]any{"ContinuousBackupsStatus": "ENABLED", "PointInTimeRecoveryDescription": pitr}
}

func (s *Service) awsDescribeBackups(q *awsapi.Req) (any, error) {
	var in tableNameIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeContinuousBackups", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	t, err := s.getTable(in.TableName)
	if err != nil {
		return nil, errf("TableNotFoundException", "Table not found: %s", in.TableName)
	}
	return map[string]any{"ContinuousBackupsDescription": s.backupsDesc(t)}, nil
}

func (s *Service) awsUpdateBackups(q *awsapi.Req) (any, error) {
	var in struct {
		TableName                        string
		PointInTimeRecoverySpecification struct{ PointInTimeRecoveryEnabled bool }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:UpdateContinuousBackups", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if _, err := s.getTable(in.TableName); err != nil {
		return nil, errf("TableNotFoundException", "Table not found: %s", in.TableName)
	}
	t, err := s.alterTable(in.TableName, func(t *Table, _ *bolt.Tx) error {
		t.PITR = in.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ContinuousBackupsDescription": s.backupsDesc(t)}, nil
}

// tableFromARN resolves a table ARN (or an index/stream ARN under it).
func (s *Service) tableFromARN(arn string) (string, error) {
	i := strings.Index(arn, ":table/")
	if !strings.HasPrefix(arn, "arn:") || i < 0 {
		return "", validation("Invalid TableArn: Invalid ResourceArn provided as input %s", arn)
	}
	name := arn[i+len(":table/"):]
	if j := strings.IndexByte(name, '/'); j >= 0 {
		name = name[:j]
	}
	if s.tableARN(name) != arn {
		return "", errf("AccessDeniedException", "The resource %s is not in this account and region", arn)
	}
	return name, nil
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		Tags        []tagIn
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:TagResource", in.ResourceArn); err != nil {
		return nil, err
	}
	name, err := s.tableFromARN(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if _, err := s.getTable(name); err != nil {
		return nil, errNotFoundGeneric
	}
	_, err = s.alterTable(name, func(t *Table, _ *bolt.Tx) error {
		if t.Tags == nil {
			t.Tags = core.Tags{}
		}
		for _, tg := range in.Tags {
			if tg.Key == "" || strings.HasPrefix(tg.Key, "aws:") {
				return validation("Invalid tag key: %q", tg.Key)
			}
			t.Tags[tg.Key] = tg.Value
		}
		if len(t.Tags) > 50 {
			return errf("LimitExceededException", "The number of tags on the resource would exceed 50")
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		TagKeys     []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:UntagResource", in.ResourceArn); err != nil {
		return nil, err
	}
	name, err := s.tableFromARN(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if _, err := s.getTable(name); err != nil {
		return nil, errNotFoundGeneric
	}
	_, err = s.alterTable(name, func(t *Table, _ *bolt.Tx) error {
		for _, k := range in.TagKeys {
			delete(t.Tags, k)
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		NextToken   string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:ListTagsOfResource", in.ResourceArn); err != nil {
		return nil, err
	}
	name, err := s.tableFromARN(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	t, err := s.getTable(name)
	if err != nil {
		return nil, errNotFoundGeneric
	}
	keys := make([]string, 0, len(t.Tags))
	for k := range t.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	tags := []map[string]string{}
	for _, k := range keys {
		tags = append(tags, map[string]string{"Key": k, "Value": t.Tags[k]})
	}
	return map[string]any{"Tags": tags}, nil
}

func (s *Service) awsDescribeLimits(q *awsapi.Req) (any, error) {
	if err := q.Authorize("dynamodb:DescribeLimits", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"AccountMaxReadCapacityUnits": 80000, "AccountMaxWriteCapacityUnits": 80000,
		"TableMaxReadCapacityUnits": 40000, "TableMaxWriteCapacityUnits": 40000}, nil
}

func (s *Service) awsDescribeEndpoints(q *awsapi.Req) (any, error) {
	if err := q.Authorize("dynamodb:DescribeEndpoints", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"Endpoints": []map[string]any{{"Address": q.R.Host, "CachePeriodInMinutes": 1440}}}, nil
}

// Read-only answers for features HomeCloud does not implement, so tools that
// read them while refreshing a table (Terraform) see them as disabled.

func (s *Service) awsDescribeKinesis(q *awsapi.Req) (any, error) {
	var in tableNameIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeKinesisStreamingDestination", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if _, err := s.getTable(in.TableName); err != nil {
		return nil, err
	}
	return map[string]any{"TableName": in.TableName, "KinesisDataStreamDestinations": []any{}}, nil
}

func (s *Service) awsDescribeContributorInsights(q *awsapi.Req) (any, error) {
	var in struct{ TableName, IndexName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeContributorInsights", s.tableARN(in.TableName)); err != nil {
		return nil, err
	}
	if _, err := s.getTable(in.TableName); err != nil {
		return nil, err
	}
	res := map[string]any{"TableName": in.TableName, "ContributorInsightsStatus": "DISABLED"}
	if in.IndexName != "" {
		res["IndexName"] = in.IndexName
	}
	return res, nil
}

// ---- consumed capacity ----

func capacityUnits(bytes int, write bool, consistent bool) float64 {
	if write {
		u := math.Ceil(float64(bytes) / 1024)
		if u < 1 {
			u = 1
		}
		return u
	}
	u := math.Ceil(float64(bytes) / 4096)
	if u < 1 {
		u = 1
	}
	if !consistent {
		u /= 2
	}
	return u
}

// consumed builds a ConsumedCapacity element (nil when not requested).
func consumed(mode, table string, units float64, write bool, index string, local bool) map[string]any {
	if mode != "TOTAL" && mode != "INDEXES" {
		return nil
	}
	c := map[string]any{"TableName": table, "CapacityUnits": units}
	if write {
		c["WriteCapacityUnits"] = units
	} else {
		c["ReadCapacityUnits"] = units
	}
	if mode == "INDEXES" {
		part := map[string]any{"CapacityUnits": units}
		if index == "" {
			c["Table"] = part
		} else {
			c["Table"] = map[string]any{"CapacityUnits": 0}
			key := "GlobalSecondaryIndexes"
			if local {
				key = "LocalSecondaryIndexes"
			}
			c[key] = map[string]any{index: part}
		}
	}
	return c
}

func validReturnCapacity(mode string) error {
	switch mode {
	case "", "NONE", "TOTAL", "INDEXES":
		return nil
	}
	return validation("1 validation error detected: Value '%s' at 'returnConsumedCapacity' failed to satisfy constraint: Member must satisfy enum value set: [INDEXES, TOTAL, NONE]", mode)
}
