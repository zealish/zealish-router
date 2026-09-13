package storage

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	// A nested path also exercises parent-directory creation.
	path := filepath.Join(t.TempDir(), "nested", "router.db")

	store, err := OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleKey(id, hash string) APIKey {
	return APIKey{
		ID:        id,
		Name:      "test-" + id,
		KeyHash:   hash,
		Enabled:   true,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestOpenSQLiteCreatesParentDirAndSchema(t *testing.T) {
	store := newTestStore(t)

	var journal string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode = %q, want wal", journal)
	}

	for _, table := range []string{"api_keys", "providers", "model_aliases"} {
		var name string
		err := store.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "router.db")
	ctx := context.Background()

	first, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.APIKeys().Create(ctx, sampleKey("k1", "hash-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = first.Close()

	// Reopening must re-run migrate() without dropping existing data.
	second, err := OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = second.Close() }()

	if _, err := second.APIKeys().GetByHash(ctx, "hash-1"); err != nil {
		t.Fatalf("data lost across reopen: %v", err)
	}

	want, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}

	var applied int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(want) {
		t.Errorf("applied migrations = %d, want %d", applied, len(want))
	}
}

func TestAPIKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	keys := newTestStore(t).APIKeys()

	want := sampleKey("k1", "hash-1")
	if err := keys.Create(ctx, want); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := keys.GetByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if got.ID != want.ID || got.Name != want.Name || !got.Enabled {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if !got.LastUsedAt.IsZero() {
		t.Errorf("LastUsedAt = %v, want zero for a fresh key", got.LastUsedAt)
	}
}

func TestGetByHashUnknownReturnsNotFound(t *testing.T) {
	keys := newTestStore(t).APIKeys()

	if _, err := keys.GetByHash(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListIsOrderedByCreation(t *testing.T) {
	ctx := context.Background()
	keys := newTestStore(t).APIKeys()

	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"c", "a", "b"} {
		k := sampleKey(id, "hash-"+id)
		k.CreatedAt = now.Add(time.Duration(i) * time.Second)
		if err := keys.Create(ctx, k); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
	}

	list, err := keys.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	for i, want := range []string{"c", "a", "b"} {
		if list[i].ID != want {
			t.Errorf("list[%d] = %s, want %s", i, list[i].ID, want)
		}
	}
}

func TestTouchLastUsed(t *testing.T) {
	ctx := context.Background()
	keys := newTestStore(t).APIKeys()

	if err := keys.Create(ctx, sampleKey("k1", "hash-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	at := time.Now().UTC().Truncate(time.Second)
	if err := keys.TouchLastUsed(ctx, "k1", at); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}

	got, err := keys.GetByHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if !got.LastUsedAt.Equal(at) {
		t.Errorf("LastUsedAt = %v, want %v", got.LastUsedAt, at)
	}

	if err := keys.TouchLastUsed(ctx, "missing", at); !errors.Is(err, ErrNotFound) {
		t.Errorf("touching an unknown id: err = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	keys := newTestStore(t).APIKeys()

	if err := keys.Create(ctx, sampleKey("k1", "hash-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := keys.Delete(ctx, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := keys.GetByHash(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("key still present: %v", err)
	}
	if err := keys.Delete(ctx, "k1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestDuplicateHashRejected(t *testing.T) {
	ctx := context.Background()
	keys := newTestStore(t).APIKeys()

	if err := keys.Create(ctx, sampleKey("k1", "same")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := keys.Create(ctx, sampleKey("k2", "same")); err == nil {
		t.Fatal("expected a uniqueness violation on key_hash")
	}
}
