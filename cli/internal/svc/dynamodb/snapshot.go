package dynamodb

import (
	"io"

	bolt "go.etcd.io/bbolt"
)

// Snapshot writes a consistent copy of the item database (for backups).
func (s *Service) Snapshot(w io.Writer) error {
	return s.db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(w)
		return err
	})
}
