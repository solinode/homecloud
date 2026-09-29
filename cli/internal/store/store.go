// Package store is HomeCloud's persistent state: a set of named collections of
// JSON documents, kept in memory and flushed atomically to a single file.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	mu   sync.RWMutex
	path string
	data map[string]map[string]json.RawMessage
}

// Open loads the store from path, creating an empty one if the file is missing.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: map[string]map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("corrupt state file %s: %w", path, err)
		}
	}
	return s, nil
}

// flush must be called with mu held.
func (s *Store) flush() error {
	b, err := json.MarshalIndent(s.data, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func Put[T any](s *Store, coll, id string, v T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[coll] == nil {
		s.data[coll] = map[string]json.RawMessage{}
	}
	s.data[coll][id] = b
	return s.flush()
}

func Get[T any](s *Store, coll, id string) (T, error) {
	var v T
	s.mu.RLock()
	b, ok := s.data[coll][id]
	s.mu.RUnlock()
	if !ok {
		return v, ErrNotFound
	}
	err := json.Unmarshal(b, &v)
	return v, err
}

func Has(s *Store, coll, id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[coll][id]
	return ok
}

// List returns every document in coll, ordered by id.
func List[T any](s *Store, coll string) []T {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.data[coll]))
	for id := range s.data[coll] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]T, 0, len(ids))
	for _, id := range ids {
		var v T
		if json.Unmarshal(s.data[coll][id], &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

func Delete(s *Store, coll, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[coll][id]; !ok {
		return ErrNotFound
	}
	delete(s.data[coll], id)
	return s.flush()
}

// Update applies fn to the stored document under a write lock and persists the result.
func Update[T any](s *Store, coll, id string, fn func(*T) error) (T, error) {
	var v T
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[coll][id]
	if !ok {
		return v, ErrNotFound
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, err
	}
	if err := fn(&v); err != nil {
		return v, err
	}
	nb, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	s.data[coll][id] = nb
	return v, s.flush()
}

// Retain keeps only the documents in coll for which keep returns true.
func (s *Store) Retain(coll string, keep func(id string, raw json.RawMessage) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for id, raw := range s.data[coll] {
		if !keep(id, raw) {
			delete(s.data[coll], id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.flush()
}
