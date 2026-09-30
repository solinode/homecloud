package cmd

import (
	"errors"
	"testing"
)

func TestServiceDataDirUnderSudo(t *testing.T) {
	home := func(u string) (string, error) {
		if u == "alice" {
			return "/home/alice", nil
		}
		return "", errors.New("unknown user")
	}
	cases := []struct {
		name     string
		explicit bool
		flag     string
		euid     int
		sudo     string
		env      string
		want     string
		wantErr  bool
	}{
		{"plain user", false, "/home/bob/.homecloud", 1000, "", "", "/home/bob/.homecloud", false},
		{"sudo uses invoking user's home", false, "/root/.homecloud", 0, "alice", "", "/home/alice/.homecloud", false},
		{"explicit flag wins", true, "/srv/hc", 0, "alice", "", "/srv/hc", false},
		{"env dir wins", false, "/srv/env", 0, "alice", "/srv/env", "/srv/env", false},
		{"real root", false, "/root/.homecloud", 0, "", "", "/root/.homecloud", false},
		{"sudo from root", false, "/root/.homecloud", 0, "root", "", "/root/.homecloud", false},
		{"unknown sudo user refuses", false, "/root/.homecloud", 0, "ghost", "", "", true},
	}
	for _, c := range cases {
		got, err := serviceDataDir(c.explicit, c.flag, c.euid, c.sudo, c.env, home)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got %q, %v; want %q (err %v)", c.name, got, err, c.want, c.wantErr)
		}
	}
}
