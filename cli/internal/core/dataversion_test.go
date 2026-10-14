package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.5.0", "0.4.1", 1}, {"v0.4.1", "0.4.1", 0}, {"0.4.1", "0.10.0", -1},
		{"1.0.0", "0.99.99", 1}, {"0.5.0-rc1", "0.5.0", 0}, {"v0.4.1-3-gabcdef", "0.4.1", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsDevVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"": true, "dev": true, "test": true, "0.1.0-dev": true, "abc1234": true, "0.5": true,
		"0.5.0": false, "v0.5.0": false, "0.5.0-rc1": false, "v0.4.1-3-gabcdef": false,
	} {
		if got := IsDevVersion(v); got != want {
			t.Errorf("IsDevVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestDataVersionStampAndCheck(t *testing.T) {
	dir := t.TempDir()
	// No stamp: data of 0.4.x or a new directory. Anything starts.
	if err := CheckDataVersion(dir, "0.4.1"); err != nil {
		t.Fatal(err)
	}
	if err := StampDataVersion(dir, "v0.5.0"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := ReadDataVersion(dir)
	if err != nil || !ok || v.Version != "0.5.0" || v.UpdatedAt.IsZero() {
		t.Fatalf("stamp = %+v %v %v", v, ok, err)
	}
	for _, same := range []string{"0.5.0", "v0.5.0", "0.5.1", "0.5.0-rc2", "dev"} {
		if err := CheckDataVersion(dir, same); err != nil {
			t.Errorf("%s refused: %v", same, err)
		}
	}
	err = CheckDataVersion(dir, "0.4.1")
	var newer *NewerDataError
	if !errors.As(err, &newer) || newer.DataVersion != "0.5.0" || newer.Version != "0.4.1" {
		t.Fatalf("0.4.1 on 0.5.0 data: %v", err)
	}
	if !strings.Contains(err.Error(), "homecloud upgrade --rollback") || !strings.Contains(err.Error(), "upgrade --version v0.5.0") {
		t.Errorf("message does not say what to do: %s", err)
	}

	// Older and development builds never lower the stamp; newer ones raise it.
	for _, v := range []string{"0.4.1", "0.1.0-dev", "dev"} {
		if err := StampDataVersion(dir, v); err != nil {
			t.Fatal(err)
		}
	}
	if v, _, _ := ReadDataVersion(dir); v.Version != "0.5.0" {
		t.Fatalf("stamp lowered to %s", v.Version)
	}
	if err := StampDataVersion(dir, "0.6.0"); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := ReadDataVersion(dir); v.Version != "0.6.0" {
		t.Fatalf("stamp = %s, want 0.6.0", v.Version)
	}

	// A corrupt stamp is an error, not silently ignored.
	os.WriteFile(filepath.Join(dir, DataVersionFile), []byte("{"), 0o600)
	if err := CheckDataVersion(dir, "0.6.0"); err == nil {
		t.Fatal("corrupt stamp accepted")
	}
}
