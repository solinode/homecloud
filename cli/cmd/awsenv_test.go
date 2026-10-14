package cmd

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/client"
)

// aws-env names the local server "localhost", not 127.0.0.1: S3 Control
// (which the Terraform AWS provider calls for every bucket) puts the account
// ID in front of the endpoint's host, which only works with a host name.
func TestAWSEnvUsesLocalhost(t *testing.T) {
	t.Setenv("HOMECLOUD_DATA_DIR", t.TempDir())
	t.Setenv("HOMECLOUD_ENDPOINT", "")
	if err := client.Save(client.Profile{Endpoint: "http://127.0.0.1:18080", AccessKeyID: "AK", SecretAccessKey: "SK"}); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	RootCmd.SetArgs([]string{"aws-env"})
	runErr := RootCmd.Execute()
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(string(out), `export AWS_ENDPOINT_URL="http://localhost:18080"`) || !strings.Contains(string(out), `export AWS_ACCESS_KEY_ID="AK"`) {
		t.Fatalf("aws-env printed:\n%s", out)
	}
	// The CLI itself keeps talking to the recorded address.
	if p, _ := client.Load(); p.Endpoint != "http://127.0.0.1:18080" {
		t.Fatalf("credentials endpoint changed: %s", p.Endpoint)
	}
}
