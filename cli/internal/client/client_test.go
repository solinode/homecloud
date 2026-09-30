package client

import "testing"

func TestConfigureKeepsRegionAndCA(t *testing.T) {
	t.Setenv("HOMECLOUD_DATA_DIR", t.TempDir())
	if err := Save(Profile{Endpoint: "https://a:8080", AccessKeyID: "AK", SecretAccessKey: "old", Region: "eu-west-1", CAFile: "/etc/ca.pem"}); err != nil {
		t.Fatal(err)
	}
	// What `homecloud configure --secret-access-key new` does.
	if err := Save(Merge(LoadFile(), Profile{SecretAccessKey: "new"})); err != nil {
		t.Fatal(err)
	}
	got := LoadFile()
	want := Profile{Endpoint: "https://a:8080", AccessKeyID: "AK", SecretAccessKey: "new", Region: "eu-west-1", CAFile: "/etc/ca.pem"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// --ca-file overrides.
	if got := Merge(want, Profile{CAFile: "/other.pem"}); got.CAFile != "/other.pem" || got.Region != "eu-west-1" {
		t.Fatalf("override: %+v", got)
	}
}
