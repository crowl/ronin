package sqlite_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
)

// Native CI exercises Windows drive letters as well as URI escaping. Check the
// actual filename so a successful connection to the wrong database cannot pass.
func TestDatabasePathEscaping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions #100% & é.db")
	store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), dir, session.Metadata{Title: "persisted"})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(t.Context(), sqlite.StoreConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, _, found, err := store.Load(t.Context(), created.ID)
	if err != nil || !found || loaded.Title != "persisted" {
		t.Fatalf("reopen: %+v, found=%v, err=%v", loaded, found, err)
	}
}
