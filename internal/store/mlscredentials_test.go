package store_test

import (
	"path/filepath"
	"testing"

	"github.com/wavedevgit/waveycord/internal/db"
	"github.com/wavedevgit/waveycord/internal/store"
)

// TestMLSCredentialDirectory proves credentials persist per user and are readable as
// a directory, which is what the gateway validates commits against.
func TestMLSCredentialDirectory(t *testing.T) {
	handle, err := db.Open(filepath.Join(t.TempDir(), "creds.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	st := store.New(handle)
	alice, err := st.CreateUser(store.CreateUserParams{
		Username: "credalice", Email: strPtr("credalice@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(store.CreateUserParams{
		Username: "credbob", Email: strPtr("credbob@example.test"), PasswordHash: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMLSCredential(alice.ID, "AAAA", "bbbb"); err != nil {
		t.Fatalf("set alice: %v", err)
	}
	if err := st.SetMLSCredential(bob.ID, "CCCC", "dddd"); err != nil {
		t.Fatalf("set bob: %v", err)
	}
	// Upsert replaces the stored material.
	if err := st.SetMLSCredential(alice.ID, "EEEE", "ffff"); err != nil {
		t.Fatalf("update alice: %v", err)
	}
	got, err := st.MLSCredential(alice.ID)
	if err != nil || got.MediaKey != "EEEE" || got.SigningKey != "ffff" {
		t.Fatalf("alice credential = %+v err=%v", got, err)
	}
	all, err := st.MLSCredentials()
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	if len(all) != 2 || all[alice.ID] != "ffff" || all[bob.ID] != "dddd" {
		t.Fatalf("directory = %+v", all)
	}
	if _, err := st.MLSCredential("missing"); err == nil {
		t.Fatal("missing credential returned no error")
	}
}
