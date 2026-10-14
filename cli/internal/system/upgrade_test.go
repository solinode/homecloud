package system

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpgradeBackupsArePruned(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"state.json": "{}"})
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := SaveUpgradeBackup(dir, UpgradeBackup{From: "0.4." + string(rune('0'+i)), To: "0.5.0", CreatedAt: start.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	// Leftovers of an interrupted copy go too.
	os.MkdirAll(filepath.Join(dir, UpgradeBackupsDir, "0.4.9-x.partial"), 0o700)
	if err := PruneUpgradeBackups(dir, KeepUpgradeBackups); err != nil {
		t.Fatal(err)
	}
	backups, err := ListUpgradeBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 3 || backups[0].From != "0.4.4" || backups[2].From != "0.4.2" {
		t.Fatalf("kept %+v", backups)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, UpgradeBackupsDir))
	if len(entries) != 3 {
		t.Fatalf("upgrade-backups holds %d entries", len(entries))
	}
}

func TestUpgradeBackupOfBusyDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := bolt.Open(filepath.Join(dir, "dynamodb.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	put := func(i int) error {
		return db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists([]byte("t"))
			if err != nil {
				return err
			}
			return b.Put([]byte(time.Now().String()+string(rune(i))), make([]byte, 2048))
		})
	}
	for i := 0; i < 200; i++ {
		if err := put(i); err != nil {
			t.Fatal(err)
		}
	}
	// The database stays open (as under a running server) and keeps changing.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				_ = put(i)
			}
		}
	}()
	b, err := SaveUpgradeBackup(dir, UpgradeBackup{From: "0.4.1", To: "0.5.0"})
	close(stop)
	<-done
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := checkBolt(filepath.Join(b.Dir, "dynamodb.db")); err != nil {
		t.Fatalf("backed-up database is inconsistent: %v", err)
	}
}

func TestCheckBoltDetectsDamage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.db")
	db, err := bolt.Open(p, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		db.Update(func(tx *bolt.Tx) error {
			b, _ := tx.CreateBucketIfNotExists([]byte{byte(i)})
			return b.Put([]byte("k"), make([]byte, 4096))
		})
	}
	db.Close()
	if !isBolt(p) {
		t.Fatal("isBolt = false for a bbolt file")
	}
	if err := checkBolt(p); err != nil {
		t.Fatalf("intact database: %v", err)
	}
	// Zero everything after the meta pages, as a copy torn by a concurrent write could.
	b, _ := os.ReadFile(p)
	page := os.Getpagesize()
	for i := 2 * page; i < len(b); i++ {
		b[i] = 0
	}
	os.WriteFile(p, b, 0o600)
	if err := checkBolt(p); err == nil {
		t.Fatal("damaged database passes")
	}
	other := filepath.Join(dir, "plain.db")
	os.WriteFile(other, []byte("not bolt at all, just a file"), 0o600)
	if isBolt(other) {
		t.Fatal("isBolt = true for a plain file")
	}
}

func TestRestoreUpgradeBackupFailsCleanly(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"state.json": `{"v":1}`})
	b, err := SaveUpgradeBackup(dir, UpgradeBackup{From: "0.4.1", To: "0.5.0"})
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, dir, map[string]string{"state.json": `{"v":2}`})
	os.RemoveAll(b.Dir) // the backup vanished
	if _, err := RestoreUpgradeBackup(dir, b, "0.5.0"); err == nil {
		t.Fatal("restore of a missing backup succeeded")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "state.json")); string(got) != `{"v":2}` {
		t.Fatalf("state = %s after a failed restore", got)
	}
}
