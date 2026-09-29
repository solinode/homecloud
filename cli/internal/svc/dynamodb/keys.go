package dynamodb

import (
	"hash/fnv"
	"strings"
)

// Keys are stored in bbolt so that byte order is DynamoDB order: each key
// attribute is encoded order-preservingly (S and B as raw bytes, N with
// decimal.sortBytes), escaped (0x00 -> 0x00 0xFF) and terminated with 0x00 0x01,
// so a partition key's encoding is a prefix of all its items' keys.

func escapeComponent(dst, raw []byte) []byte {
	for _, c := range raw {
		if c == 0 {
			dst = append(dst, 0, 0xFF)
		} else {
			dst = append(dst, c)
		}
	}
	return dst
}

func appendComponent(dst []byte, v AV) []byte {
	return append(escapeComponent(dst, rawKeyBytes(v)), 0, 1)
}

func rawKeyBytes(v AV) []byte {
	switch v.Kind {
	case kS:
		return []byte(v.S)
	case kB:
		return v.B
	case kN:
		d, _ := parseDecimal(v.S)
		return d.sortBytes()
	}
	return nil
}

// prefixSuccessor returns the smallest key greater than every key with prefix p.
func prefixSuccessor(p []byte) []byte {
	out := append([]byte(nil), p...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] < 0xFF {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}

// KeyDef is a key attribute: name and scalar type (S, N or B).
type KeyDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// schema is a (table or index) key schema.
type schema struct {
	PK KeyDef
	SK *KeyDef
}

func (s schema) names() []string {
	if s.SK != nil {
		return []string{s.PK.Name, s.SK.Name}
	}
	return []string{s.PK.Name}
}

func (s schema) has(name string) bool {
	return s.PK.Name == name || (s.SK != nil && s.SK.Name == name)
}

// checkKeyAttr validates a key attribute value in an item or key.
func checkKeyAttr(def KeyDef, v AV, index string) error {
	if v.Kind.String() != def.Type {
		if index != "" {
			return invalidParam("Type mismatch for Index Key %s Expected: %s Actual: %s IndexName: %s", def.Name, def.Type, v.Kind, index)
		}
		return invalidParam("Type mismatch for key %s expected: %s actual: %s", def.Name, def.Type, v.Kind)
	}
	switch v.Kind {
	case kS:
		if v.S == "" {
			if index != "" {
				return invalidParam("One or more parameter values are not valid. A value specified for a secondary index key is not supported. The AttributeValue for a key attribute cannot contain an empty string value. IndexName: %s, IndexKey: %s", index, def.Name)
			}
			return invalidParam("The AttributeValue for a key attribute cannot contain an empty string value. Key: %s", def.Name)
		}
		if len(v.S) > 2048 && def.Name != "" {
			return validation("One or more parameter values were invalid: Size of hashkey has exceeded the maximum size limit of2048 bytes")
		}
	case kB:
		if len(v.B) == 0 {
			return invalidParam("The AttributeValue for a key attribute cannot contain an empty binary value. Key: %s", def.Name)
		}
	}
	return nil
}

// keyOf encodes the key of an item under s. It fails if a key attribute is
// missing or has the wrong type.
func (s schema) keyOf(it Item) ([]byte, error) {
	pk, ok := it[s.PK.Name]
	if !ok {
		return nil, invalidParam("Missing the key %s in the item", s.PK.Name)
	}
	if err := checkKeyAttr(s.PK, pk, ""); err != nil {
		return nil, err
	}
	k := appendComponent(nil, pk)
	if s.SK != nil {
		sk, ok := it[s.SK.Name]
		if !ok {
			return nil, invalidParam("Missing the key %s in the item", s.SK.Name)
		}
		if err := checkKeyAttr(*s.SK, sk, ""); err != nil {
			return nil, err
		}
		k = appendComponent(k, sk)
	}
	return k, nil
}

// keyFrom validates a Key parameter: exactly the schema's attributes.
func (s schema) keyFrom(key Item) ([]byte, error) {
	if err := validateItem(key); err != nil {
		return nil, err
	}
	n := 1
	if s.SK != nil {
		n = 2
	}
	if len(key) != n {
		return nil, validation("The provided key element does not match the schema")
	}
	for _, name := range s.names() {
		if _, ok := key[name]; !ok {
			return nil, validation("The provided key element does not match the schema")
		}
	}
	return s.keyOf(key)
}

// project returns just the key attributes of it.
func (s schema) project(it Item) Item {
	out := Item{}
	for _, n := range s.names() {
		if v, ok := it[n]; ok {
			out[n] = v
		}
	}
	return out
}

// indexKeyOf encodes an item's entry in an index: index key then table key.
// ok is false when the item lacks an index key attribute (sparse index).
func indexKeyOf(ix schema, table []byte, it Item) ([]byte, bool) {
	pk, ok := it[ix.PK.Name]
	if !ok || pk.Kind.String() != ix.PK.Type {
		return nil, false
	}
	k := appendComponent(nil, pk)
	if ix.SK != nil {
		sk, ok := it[ix.SK.Name]
		if !ok || sk.Kind.String() != ix.SK.Type {
			return nil, false
		}
		k = appendComponent(k, sk)
	}
	return append(k, table...), true
}

// segmentOf assigns an item to a parallel-scan segment by partition key.
func segmentOf(key []byte, total int) int {
	// The partition component ends at the first unescaped 0x00 0x01.
	end := len(key)
	for i := 0; i+1 < len(key); i++ {
		if key[i] == 0 {
			if key[i+1] == 1 {
				end = i
				break
			}
			i++
		}
	}
	h := fnv.New32a()
	h.Write(key[:end])
	return int(h.Sum32() % uint32(total))
}

func keyTypeName(t string) string {
	switch strings.ToUpper(t) {
	case "S", "N", "B":
		return strings.ToUpper(t)
	}
	return ""
}
