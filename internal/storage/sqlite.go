package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	// Pure-Go SQLite driver: keeps CGO_ENABLED=0 so the binary stays static and
	// deployable on distroless.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// SQLite is a Store backed by a local SQLite database file.
type SQLite struct {
	db        *sql.DB
	keys      *sqliteAPIKeys
	providers *sqliteProviders
	models    *sqliteModels
	settings  *sqliteSettings
}

// OpenSQLite opens (creating if needed) the database at path, creates its
// parent directory, applies migrations and returns a ready Store.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	if path == "" {
		return nil, errors.New("storage: empty database path")
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("storage: create %s: %w", dir, err)
		}
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	// SQLite tolerates concurrent readers but a single writer; WAL plus a
	// bounded pool keeps writes serialised without lock contention errors.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: connect %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &SQLite{
		db:        db,
		keys:      &sqliteAPIKeys{db: db},
		providers: &sqliteProviders{db: db},
		models:    &sqliteModels{db: db},
		settings:  &sqliteSettings{db: db},
	}, nil
}

// dsn builds the driver connection string with WAL, a busy timeout and foreign
// key enforcement enabled.
func dsn(path string) string {
	params := url.Values{}
	params.Set("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "synchronous(NORMAL)")
	return "file:" + path + "?" + params.Encode()
}

// migrate applies every embedded migration that has not run yet, in name order.
func migrate(ctx context.Context, db *sql.DB) error {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT    PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("storage: create migration table: %w", err)
	}

	entries, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("storage: read migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		var seen int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&seen); err != nil {
			return fmt.Errorf("storage: check migration %s: %w", name, err)
		}
		if seen > 0 {
			continue
		}

		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("storage: read migration %s: %w", name, err)
		}
		if err := applyMigration(ctx, db, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, name, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin migration %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("storage: apply migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		name, time.Now().Unix()); err != nil {
		return fmt.Errorf("storage: record migration %s: %w", name, err)
	}
	return tx.Commit()
}

// APIKeys implements Store.
func (s *SQLite) APIKeys() APIKeyStore { return s.keys }

// Providers implements Store.
func (s *SQLite) Providers() ProviderStore { return s.providers }

// Models implements Store.
func (s *SQLite) Models() ModelStore { return s.models }

// Settings implements Store.
func (s *SQLite) Settings() SettingStore { return s.settings }

// Close implements Store.
func (s *SQLite) Close() error { return s.db.Close() }

type sqliteAPIKeys struct {
	db *sql.DB
}

func (s *sqliteAPIKeys) Create(ctx context.Context, key APIKey) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, key_hash, enabled, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.KeyHash, key.Enabled,
		key.CreatedAt.Unix(), unixOrZero(key.LastUsedAt))
	if err != nil {
		return fmt.Errorf("storage: create api key: %w", err)
	}
	return nil
}

func (s *sqliteAPIKeys) GetByHash(ctx context.Context, hash string) (APIKey, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, key_hash, enabled, created_at, last_used_at
		 FROM api_keys WHERE key_hash = ?`, hash)

	key, err := scanAPIKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("storage: get api key: %w", err)
	}
	return key, nil
}

func (s *sqliteAPIKeys) List(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, key_hash, enabled, created_at, last_used_at
		 FROM api_keys ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list api keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var keys []APIKey
	for rows.Next() {
		key, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan api key: %w", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list api keys: %w", err)
	}
	return keys, nil
}

func (s *sqliteAPIKeys) TouchLastUsed(ctx context.Context, id string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ?`, at.Unix(), id)
	if err != nil {
		return fmt.Errorf("storage: touch api key: %w", err)
	}
	return affectOne(res, "touch api key")
}

func (s *sqliteAPIKeys) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("storage: delete api key: %w", err)
	}
	return affectOne(res, "delete api key")
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanAPIKey(src scanner) (APIKey, error) {
	var (
		key      APIKey
		created  int64
		lastUsed int64
	)
	if err := src.Scan(&key.ID, &key.Name, &key.KeyHash, &key.Enabled, &created, &lastUsed); err != nil {
		return APIKey{}, err
	}
	key.CreatedAt = time.Unix(created, 0).UTC()
	if lastUsed > 0 {
		key.LastUsedAt = time.Unix(lastUsed, 0).UTC()
	}
	return key, nil
}

func affectOne(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: %s: %w", op, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

type sqliteProviders struct {
	db *sql.DB
}

const providerColumns = `id, name, kind, base_url, api_key, timeout_ms, enabled`

func (s *sqliteProviders) List(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+providerColumns+` FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("storage: list providers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Provider
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan provider: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list providers: %w", err)
	}
	return out, nil
}

func (s *sqliteProviders) Get(ctx context.Context, name string) (Provider, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+providerColumns+` FROM providers WHERE name = ?`, name)

	p, err := scanProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("storage: get provider: %w", err)
	}
	return p, nil
}

// Put upserts by name, which is the identity clients address providers by.
func (s *sqliteProviders) Put(ctx context.Context, p Provider) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO providers (`+providerColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   kind = excluded.kind,
		   base_url = excluded.base_url,
		   api_key = excluded.api_key,
		   timeout_ms = excluded.timeout_ms,
		   enabled = excluded.enabled`,
		p.ID, p.Name, p.Kind, p.BaseURL, p.APIKey, p.Timeout.Milliseconds(), p.Enabled)
	if err != nil {
		return fmt.Errorf("storage: put provider: %w", err)
	}
	return nil
}

func (s *sqliteProviders) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("storage: delete provider: %w", err)
	}
	return affectOne(res, "delete provider")
}

func scanProvider(src scanner) (Provider, error) {
	var (
		p         Provider
		timeoutMS int64
	)
	if err := src.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.APIKey, &timeoutMS, &p.Enabled); err != nil {
		return Provider{}, err
	}
	p.Timeout = time.Duration(timeoutMS) * time.Millisecond
	return p, nil
}

type sqliteModels struct {
	db *sql.DB
}

func (s *sqliteModels) List(ctx context.Context) ([]ModelAlias, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT alias, provider, model, fallback FROM model_aliases ORDER BY alias`)
	if err != nil {
		return nil, fmt.Errorf("storage: list model aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ModelAlias
	for rows.Next() {
		m, err := scanModelAlias(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan model alias: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list model aliases: %w", err)
	}
	return out, nil
}

func (s *sqliteModels) Get(ctx context.Context, alias string) (ModelAlias, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT alias, provider, model, fallback FROM model_aliases WHERE alias = ?`, alias)

	m, err := scanModelAlias(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ModelAlias{}, ErrNotFound
	}
	if err != nil {
		return ModelAlias{}, fmt.Errorf("storage: get model alias: %w", err)
	}
	return m, nil
}

func (s *sqliteModels) Put(ctx context.Context, m ModelAlias) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO model_aliases (alias, provider, model, fallback)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(alias) DO UPDATE SET
		   provider = excluded.provider,
		   model = excluded.model,
		   fallback = excluded.fallback`,
		m.Alias, m.Provider, m.Model, encodeFallback(m.Fallback))
	if err != nil {
		return fmt.Errorf("storage: put model alias: %w", err)
	}
	return nil
}

func (s *sqliteModels) Delete(ctx context.Context, alias string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM model_aliases WHERE alias = ?`, alias)
	if err != nil {
		return fmt.Errorf("storage: delete model alias: %w", err)
	}
	return affectOne(res, "delete model alias")
}

func scanModelAlias(src scanner) (ModelAlias, error) {
	var (
		m        ModelAlias
		fallback string
	)
	if err := src.Scan(&m.Alias, &m.Provider, &m.Model, &fallback); err != nil {
		return ModelAlias{}, err
	}
	m.Fallback = decodeFallback(fallback)
	return m, nil
}

// Fallback chains are short ordered lists of aliases, so a comma-separated
// column keeps them queryable without a join table.
func encodeFallback(chain []string) string {
	return strings.Join(chain, ",")
}

func decodeFallback(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

type sqliteSettings struct {
	db *sql.DB
}

func (s *sqliteSettings) All(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("storage: list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("storage: scan setting: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list settings: %w", err)
	}
	return out, nil
}

func (s *sqliteSettings) Put(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("storage: put setting: %w", err)
	}
	return nil
}
