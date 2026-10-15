package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

func TestSubscriptionMigrationPersistenceAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *model.State) error {
		st.Users["u"] = model.User{ID: "u", SubscriptionToken: "legacy-secret-token"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{3}, 32)
	if err := s.ProtectSubscriptions(key); err != nil {
		t.Fatal(err)
	}
	checkDisk := func(secret string) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(secret)) || bytes.Contains(b, []byte(`"subscription_token":`)) {
			t.Fatal("plaintext subscription token persisted")
		}
	}
	checkDisk("legacy-secret-token")
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.ProtectSubscriptions(bytes.Repeat([]byte{4}, 32)); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
	if err := reopened.ProtectSubscriptions(key); err != nil {
		t.Fatal(err)
	}
	user := reopened.Snapshot().Users["u"]
	if user.SubscriptionToken != "legacy-secret-token" || !auth.EqualSecretHash(user.SubscriptionTokenHash, user.SubscriptionToken) {
		t.Fatal("migration invalidated legacy subscription")
	}
	if err := reopened.Update(func(st *model.State) error {
		user := st.Users["u"]
		user.SubscriptionToken = "rotated-secret-token"
		st.Users["u"] = user
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	checkDisk("rotated-secret-token")
	if auth.EqualSecretHash(reopened.Snapshot().Users["u"].SubscriptionTokenHash, "legacy-secret-token") {
		t.Fatal("rotated subscription still accepts old token")
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.ProtectSubscriptions(key); err != nil {
		t.Fatal(err)
	}
	if again.Snapshot().Users["u"].SubscriptionToken != "rotated-secret-token" {
		t.Fatal("rotation lost on restart")
	}
}

func TestSubscriptionCiphertextIsBoundToUser(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "state.json"))
	key := bytes.Repeat([]byte{1}, 32)
	if err := s.ProtectSubscriptions(key); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(st *model.State) error {
		st.Users["a"] = model.User{ID: "a", SubscriptionToken: "secret-a"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	user := st.Users["a"]
	user.SubscriptionToken = ""
	user.ID = "b"
	st.Users = map[string]model.User{"b": user}
	if _, err := protectSubscriptionTokens(&st, s.subscriptionKey); err == nil {
		t.Fatal("ciphertext transplant accepted")
	}
}
