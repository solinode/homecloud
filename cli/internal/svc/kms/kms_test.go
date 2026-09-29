package kms

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func TestEncryptRotateDecrypt(t *testing.T) {
	env := svctest.Env(t)
	sec, err := secrets.New(env)
	if err != nil {
		t.Fatal(err)
	}
	k := New(env, sec)
	key, err := k.create("test", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := map[string]string{"tenant": "a"}
	blob, _, err := k.Encrypt(key.ID, []byte("secret"), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.rotate(key.ID, "ON_DEMAND"); err != nil {
		t.Fatal(err)
	}
	pt, _, err := k.Decrypt(blob, ctx)
	if err != nil || string(pt) != "secret" {
		t.Fatalf("decrypt after rotation: %q %v", pt, err)
	}
	if _, _, err := k.Decrypt(blob, map[string]string{"tenant": "b"}); err == nil {
		t.Fatal("decrypted with the wrong encryption context")
	}
	_, _ = store.Update(env.Store, cKeys, key.ID, func(x *Key) error { x.State = "Disabled"; return nil })
	if _, _, err := k.Decrypt(blob, ctx); err == nil {
		t.Fatal("disabled key decrypted")
	}
}

func TestManagedKeyIsStable(t *testing.T) {
	env := svctest.Env(t)
	sec, _ := secrets.New(env)
	k := New(env, sec)
	a, _ := k.ManagedKey("alias/hc/test")
	b, _ := k.ManagedKey("alias/hc/test")
	if a == "" || a != b {
		t.Fatalf("managed key changed: %s %s", a, b)
	}
}

func TestContextEncodingIsUnambiguous(t *testing.T) {
	env := svctest.Env(t)
	sec, _ := secrets.New(env)
	k := New(env, sec)
	key, _ := k.create("t", false, nil)
	blob, _, _ := k.Encrypt(key.ID, []byte("x"), map[string]string{"a": "b", "c": "d"})
	if _, _, err := k.Decrypt(blob, map[string]string{"a": "b\nc=d"}); err == nil {
		t.Fatal("a different context decrypted the blob")
	}
}
