package dynamodb

import (
	"regexp"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	bolt "go.etcd.io/bbolt"
)

const (
	cTables       = "dynamodb_tables"
	maxItemBytes  = 400 << 10
	storeVersion  = 2
	maxGSIs       = 20
	maxLSIs       = 5
	maxProjection = 100
)

// Projection selects the attributes copied into an index.
type Projection struct {
	Type             string   `json:"type"` // ALL | KEYS_ONLY | INCLUDE
	NonKeyAttributes []string `json:"non_key_attributes,omitempty"`
}

// Index is a global or local secondary index.
type Index struct {
	Name          string      `json:"name"`
	PartitionKey  KeyDef      `json:"partition_key"`
	SortKey       *KeyDef     `json:"sort_key,omitempty"`
	Projection    *Projection `json:"projection,omitempty"` // nil means ALL
	Status        string      `json:"status,omitempty"`
	ReadCapacity  int64       `json:"read_capacity,omitempty"`
	WriteCapacity int64       `json:"write_capacity,omitempty"`
}

func (ix Index) schema() schema { return schema{PK: ix.PartitionKey, SK: ix.SortKey} }

func (ix Index) projection() Projection {
	if ix.Projection == nil {
		return Projection{Type: "ALL"}
	}
	return *ix.Projection
}

// StreamInfo is one DynamoDB stream of a table. A table has at most one
// enabled stream; disabled streams stay readable for 24 hours.
type StreamInfo struct {
	Label    string     `json:"label"`
	ViewType string     `json:"view_type"`
	Created  time.Time  `json:"created"`
	Disabled *time.Time `json:"disabled,omitempty"`
}

// Table is a table's metadata (items live in bbolt).
type Table struct {
	Name               string       `json:"name"`
	ARN                string       `json:"arn"`
	ID                 string       `json:"id,omitempty"`
	PartitionKey       KeyDef       `json:"partition_key"`
	SortKey            *KeyDef      `json:"sort_key,omitempty"`
	Attributes         []KeyDef     `json:"attribute_definitions,omitempty"`
	Indexes            []Index      `json:"global_secondary_indexes"`
	LocalIndexes       []Index      `json:"local_secondary_indexes,omitempty"`
	TTLAttribute       string       `json:"ttl_attribute,omitempty"`
	Status             string       `json:"status"`
	BillingMode        string       `json:"billing_mode"`
	ReadCapacity       int64        `json:"read_capacity,omitempty"`
	WriteCapacity      int64        `json:"write_capacity,omitempty"`
	BillingUpdatedAt   *time.Time   `json:"billing_updated_at,omitempty"`
	CreatedAt          time.Time    `json:"created_at"`
	Tags               core.Tags    `json:"tags,omitempty"`
	Streams            []StreamInfo `json:"streams,omitempty"`
	PITR               bool         `json:"point_in_time_recovery,omitempty"`
	DeletionProtection bool         `json:"deletion_protection,omitempty"`
	TableClass         string       `json:"table_class,omitempty"`
	SSEKMSKey          string       `json:"sse_kms_key,omitempty"`
	SSEEnabled         bool         `json:"sse_enabled,omitempty"`
	Version            int          `json:"storage_version,omitempty"`
}

func (t *Table) schema() schema { return schema{PK: t.PartitionKey, SK: t.SortKey} }

// index finds a GSI or LSI by name.
func (t *Table) index(name string) (*Index, bool) {
	for i := range t.Indexes {
		if t.Indexes[i].Name == name {
			return &t.Indexes[i], false
		}
	}
	for i := range t.LocalIndexes {
		if t.LocalIndexes[i].Name == name {
			return &t.LocalIndexes[i], true
		}
	}
	return nil, false
}

func (t *Table) allIndexes() []Index {
	out := make([]Index, 0, len(t.Indexes)+len(t.LocalIndexes))
	out = append(out, t.Indexes...)
	return append(out, t.LocalIndexes...)
}

// stream returns the enabled stream, if any.
func (t *Table) stream() *StreamInfo {
	if n := len(t.Streams); n > 0 && t.Streams[n-1].Disabled == nil {
		return &t.Streams[n-1]
	}
	return nil
}

func (t *Table) streamARN(label string) string { return t.ARN + "/stream/" + label }

// projectFor returns the attributes of it visible through index ix.
func (t *Table) projectFor(ix *Index, it Item) Item {
	p := ix.projection()
	if p.Type == "ALL" || p.Type == "" {
		return it
	}
	out := Item{}
	keep := func(n string) {
		if v, ok := it[n]; ok {
			out[n] = v
		}
	}
	for _, n := range t.schema().names() {
		keep(n)
	}
	for _, n := range ix.schema().names() {
		keep(n)
	}
	if p.Type == "INCLUDE" {
		for _, n := range p.NonKeyAttributes {
			keep(n)
		}
	}
	return out
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,255}$`)

// validateSchema checks key definitions, index definitions and limits, and
// fills defaults (attribute definitions, projections, statuses).
func (t *Table) validateSchema() error {
	if !nameRe.MatchString(t.Name) {
		return validation("1 validation error detected: Value '%s' at 'tableName' failed to satisfy constraint: Member must satisfy regular expression pattern: [a-zA-Z0-9_.-]+ and length 3-255", t.Name)
	}
	defs := map[string]string{}
	for _, a := range t.Attributes {
		defs[a.Name] = a.Type
	}
	used := map[string]bool{}
	checkKey := func(k *KeyDef, where string) error {
		if k.Name == "" {
			return invalidParam("%s: key attribute name is required", where)
		}
		if k.Type == "" {
			k.Type = defs[k.Name]
		}
		if k.Type == "" {
			if len(t.Attributes) > 0 {
				return invalidParam("Some index key attributes are not defined in AttributeDefinitions. Keys: [%s], AttributeDefinitions: %s", k.Name, attrNames(t.Attributes))
			}
			k.Type = "S"
		}
		k.Type = keyTypeName(k.Type)
		if k.Type == "" {
			return invalidParam("Member must satisfy enum value set: [B, N, S] (attribute %s)", k.Name)
		}
		if d, ok := defs[k.Name]; ok && d != k.Type {
			return invalidParam("Attribute %s is defined with conflicting types", k.Name)
		}
		defs[k.Name] = k.Type
		used[k.Name] = true
		return nil
	}
	if err := checkKey(&t.PartitionKey, "table"); err != nil {
		return err
	}
	if t.SortKey != nil {
		if t.SortKey.Name == t.PartitionKey.Name {
			return invalidParam("Both the Hash Key and the Range Key element in the KeySchema have the same name")
		}
		if err := checkKey(t.SortKey, "table"); err != nil {
			return err
		}
	}
	if len(t.Indexes) > maxGSIs {
		return errf("LimitExceededException", "Subscriber limit exceeded: The number of global secondary indexes exceeds the limit of %d", maxGSIs)
	}
	if len(t.LocalIndexes) > maxLSIs {
		return invalidParam("Number of LocalSecondaryIndexes exceeds per-table limit of %d", maxLSIs)
	}
	names := map[string]bool{}
	for i := range t.LocalIndexes {
		ix := &t.LocalIndexes[i]
		if t.SortKey == nil {
			return invalidParam("Table KeySchema does not have a range key, which is required when specifying a LocalSecondaryIndex")
		}
		if ix.PartitionKey.Name != t.PartitionKey.Name {
			return invalidParam("Index KeySchema does not have the same leading hash key as table KeySchema for index: %s. index hash key: %s, table hash key: %s", ix.Name, ix.PartitionKey.Name, t.PartitionKey.Name)
		}
		if ix.SortKey == nil {
			return invalidParam("Index KeySchema must have a range key for local secondary index: %s", ix.Name)
		}
	}
	for i, list := 0, [][]Index{t.Indexes, t.LocalIndexes}; i < 2; i++ {
		for j := range list[i] {
			ix := &list[i][j]
			if !nameRe.MatchString(ix.Name) {
				return validation("Invalid index name %q: index names are 3-255 letters, digits, dots, hyphens or underscores", ix.Name)
			}
			if names[ix.Name] {
				return invalidParam("Duplicate index name: %s", ix.Name)
			}
			names[ix.Name] = true
			if err := checkKey(&ix.PartitionKey, "index "+ix.Name); err != nil {
				return err
			}
			if ix.SortKey != nil {
				if ix.SortKey.Name == ix.PartitionKey.Name {
					return invalidParam("Both the Hash Key and the Range Key element in the KeySchema have the same name")
				}
				if err := checkKey(ix.SortKey, "index "+ix.Name); err != nil {
					return err
				}
			}
			if err := validProjection(ix); err != nil {
				return err
			}
			if ix.Status == "" {
				ix.Status = "ACTIVE"
			}
		}
	}
	if len(t.Attributes) > 0 {
		for _, a := range t.Attributes {
			if !used[a.Name] {
				return invalidParam("Number of attributes in KeySchema does not exactly match number of attributes defined in AttributeDefinitions")
			}
		}
	}
	t.Attributes = t.Attributes[:0]
	seen := map[string]bool{}
	add := func(k *KeyDef) {
		if k != nil && !seen[k.Name] {
			seen[k.Name] = true
			t.Attributes = append(t.Attributes, *k)
		}
	}
	add(&t.PartitionKey)
	add(t.SortKey)
	for _, ix := range t.allIndexes() {
		add(&ix.PartitionKey)
		add(ix.SortKey)
	}
	if t.Indexes == nil {
		t.Indexes = []Index{}
	}
	return nil
}

func validProjection(ix *Index) error {
	if ix.Projection == nil {
		ix.Projection = &Projection{Type: "ALL"}
	}
	p := ix.Projection
	switch p.Type {
	case "ALL", "KEYS_ONLY":
		if len(p.NonKeyAttributes) > 0 {
			return invalidParam("ProjectionType is %s, but NonKeyAttributes is specified", p.Type)
		}
	case "INCLUDE":
		if len(p.NonKeyAttributes) == 0 {
			return invalidParam("ProjectionType is INCLUDE, but NonKeyAttributes is not specified")
		}
		if len(p.NonKeyAttributes) > maxProjection {
			return invalidParam("The number of projected attributes exceeds %d", maxProjection)
		}
	case "":
		p.Type = "ALL"
	default:
		return invalidParam("Unknown ProjectionType: %s", p.Type)
	}
	return nil
}

func attrNames(defs []KeyDef) string {
	s := "["
	for i, d := range defs {
		if i > 0 {
			s += ", "
		}
		s += d.Name
	}
	return s + "]"
}

func newTableID() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// getTable loads a table's metadata.
func (s *Service) getTable(name string) (*Table, error) {
	t, err := store.Get[Table](s.env.Store, cTables, name)
	if err != nil {
		return nil, tableNotFound(name)
	}
	return &t, nil
}

// createTable persists a validated table and creates its storage.
func (s *Service) createTable(t *Table) error {
	if err := t.validateSchema(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if store.Has(s.env.Store, cTables, t.Name) {
		return errf("ResourceInUseException", "Table already exists: %s", t.Name)
	}
	now := s.now()
	t.ARN = s.env.ARN("dynamodb", "table/"+t.Name)
	t.ID = newTableID()
	t.Status = "ACTIVE"
	t.CreatedAt = now
	t.Version = storeVersion
	if t.BillingMode == "" {
		t.BillingMode = "PAY_PER_REQUEST"
	}
	if t.TableClass == "" {
		t.TableClass = "STANDARD"
	}
	if st := t.stream(); st != nil {
		st.Created = now
		st.Label = streamLabel(now)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(tableBucketName(t.Name)) != nil {
			if err := tx.DeleteBucket(tableBucketName(t.Name)); err != nil {
				return err
			}
		}
		_, err := ensureTableBuckets(tx, t)
		return err
	}); err != nil {
		return err
	}
	return store.Put(s.env.Store, cTables, t.Name, *t)
}

func streamLabel(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000") }

// deleteTable removes a table and its items.
func (s *Service) deleteTable(name string) (*Table, int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getTable(name)
	if err != nil {
		return nil, 0, 0, err
	}
	if t.DeletionProtection {
		return nil, 0, 0, validation("Resource cannot be deleted as it is currently protected against manual deletion. Disable deletion protection first.")
	}
	count, size := s.stats(t)
	if err := store.Delete(s.env.Store, cTables, t.Name); err != nil {
		return nil, 0, 0, err
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(tableBucketName(t.Name)) == nil {
			return nil
		}
		return tx.DeleteBucket(tableBucketName(t.Name))
	})
	t.Status = "DELETING"
	return t, count, size, err
}

// alterTable changes table metadata under the schema lock. fn may also change
// storage (e.g. backfill an index) in the bbolt transaction it is given.
func (s *Service) alterTable(name string, fn func(t *Table, tx *bolt.Tx) error) (*Table, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.getTable(name)
	if err != nil {
		return nil, err
	}
	if err := s.db.Update(func(tx *bolt.Tx) error { return fn(t, tx) }); err != nil {
		return nil, err
	}
	if err := store.Put(s.env.Store, cTables, t.Name, *t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Service) listTableNames() []string {
	var out []string
	for _, t := range store.List[Table](s.env.Store, cTables) {
		out = append(out, t.Name)
	}
	return out
}
