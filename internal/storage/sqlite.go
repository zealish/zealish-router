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
	combos    *sqliteCombos
	proxies   *sqliteProxies
	settings  *sqliteSettings
	usage     *sqliteUsage
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
		combos:    &sqliteCombos{db: db},
		proxies:   &sqliteProxies{db: db},
		settings:  &sqliteSettings{db: db},
		usage:     &sqliteUsage{db: db},
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

// Combos implements Store.
func (s *SQLite) Combos() ComboStore { return s.combos }

// Proxies implements Store.
func (s *SQLite) Proxies() ProxyStore { return s.proxies }

// Settings implements Store.
func (s *SQLite) Settings() SettingStore { return s.settings }

// Usage implements Store.
func (s *SQLite) Usage() UsageStore { return s.usage }

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
		`SELECT `+apiKeyColumns+`
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
		`SELECT `+apiKeyColumns+`
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

func (s *sqliteAPIKeys) SetQuota(ctx context.Context, id string, perMin int, budgetUSD float64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET rate_limit_per_min = ?, monthly_budget_usd = ? WHERE id = ?`,
		perMin, budgetUSD, id)
	if err != nil {
		return fmt.Errorf("storage: set api key quota: %w", err)
	}
	return affectOne(res, "set api key quota")
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

const apiKeyColumns = `id, name, key_hash, enabled, created_at, last_used_at,
	rate_limit_per_min, monthly_budget_usd`

func scanAPIKey(src scanner) (APIKey, error) {
	var (
		key      APIKey
		created  int64
		lastUsed int64
	)
	if err := src.Scan(&key.ID, &key.Name, &key.KeyHash, &key.Enabled, &created, &lastUsed,
		&key.RateLimitPerMin, &key.MonthlyBudgetUSD); err != nil {
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

const providerColumns = `id, name, kind, base_url, api_key, timeout_ms, enabled, alias_prefix, provider_group, catalog_id, use_proxy_pool`

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
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   kind = excluded.kind,
		   base_url = excluded.base_url,
		   api_key = excluded.api_key,
		   timeout_ms = excluded.timeout_ms,
		   enabled = excluded.enabled,
		   alias_prefix = excluded.alias_prefix,
		   provider_group = excluded.provider_group,
		   catalog_id = excluded.catalog_id,
		   use_proxy_pool = excluded.use_proxy_pool`,
		p.ID, p.Name, p.Kind, p.BaseURL, p.APIKey, p.Timeout.Milliseconds(), p.Enabled, p.AliasPrefix,
		p.Group, p.CatalogID, p.UseProxyPool)
	if err != nil {
		return fmt.Errorf("storage: put provider: %w", err)
	}
	return nil
}

// Delete removes the provider and everything that only exists because of it:
// its model aliases, references to those aliases in other fallback chains and
// combos, and its usage log. Nothing orphaned survives the provider it belongs
// to.
func (s *sqliteProviders) Delete(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: delete provider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM providers WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("storage: delete provider: %w", err)
	}
	if err := affectOne(res, "delete provider"); err != nil {
		return err
	}

	dropped, err := deleteProviderAliases(ctx, tx, name)
	if err != nil {
		return err
	}
	if err := pruneFallbacks(ctx, tx, dropped); err != nil {
		return err
	}
	if err := pruneComboMembers(ctx, tx, dropped); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_events WHERE provider = ?`, name); err != nil {
		return fmt.Errorf("storage: delete provider usage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: delete provider: %w", err)
	}
	return nil
}

// deleteProviderAliases removes the provider's aliases and reports their names.
func deleteProviderAliases(ctx context.Context, tx *sql.Tx, provider string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT alias FROM model_aliases WHERE provider = ?`, provider)
	if err != nil {
		return nil, fmt.Errorf("storage: list provider aliases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	dropped := map[string]bool{}
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, fmt.Errorf("storage: scan provider alias: %w", err)
		}
		dropped[alias] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list provider aliases: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM model_aliases WHERE provider = ?`, provider); err != nil {
		return nil, fmt.Errorf("storage: delete provider aliases: %w", err)
	}
	return dropped, nil
}

// pruneFallbacks strips deleted aliases from the fallback chains that still
// point at them, so routing never resolves a dangling alias.
func pruneFallbacks(ctx context.Context, tx *sql.Tx, dropped map[string]bool) error {
	if len(dropped) == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		`SELECT alias, fallback FROM model_aliases WHERE fallback <> ''`)
	if err != nil {
		return fmt.Errorf("storage: list fallback chains: %w", err)
	}
	defer func() { _ = rows.Close() }()

	updates := map[string]string{}
	for rows.Next() {
		var alias, fallback string
		if err := rows.Scan(&alias, &fallback); err != nil {
			return fmt.Errorf("storage: scan fallback chain: %w", err)
		}
		chain := decodeFallback(fallback)
		kept := make([]string, 0, len(chain))
		for _, ref := range chain {
			if !dropped[ref] {
				kept = append(kept, ref)
			}
		}
		if len(kept) != len(chain) {
			updates[alias] = encodeFallback(kept)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storage: list fallback chains: %w", err)
	}

	for alias, fallback := range updates {
		if _, err := tx.ExecContext(ctx,
			`UPDATE model_aliases SET fallback = ? WHERE alias = ?`, fallback, alias); err != nil {
			return fmt.Errorf("storage: prune fallback chain: %w", err)
		}
	}
	return nil
}

func scanProvider(src scanner) (Provider, error) {
	var (
		p         Provider
		timeoutMS int64
	)
	if err := src.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.APIKey, &timeoutMS, &p.Enabled,
		&p.AliasPrefix, &p.Group, &p.CatalogID, &p.UseProxyPool); err != nil {
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

type sqliteCombos struct {
	db *sql.DB
}

func (s *sqliteCombos) List(ctx context.Context) ([]Combo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, strategy, members, enabled FROM combos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("storage: list combos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Combo
	for rows.Next() {
		c, err := scanCombo(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan combo: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list combos: %w", err)
	}
	return out, nil
}

func (s *sqliteCombos) Get(ctx context.Context, name string) (Combo, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT name, strategy, members, enabled FROM combos WHERE name = ?`, name)

	c, err := scanCombo(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Combo{}, ErrNotFound
	}
	if err != nil {
		return Combo{}, fmt.Errorf("storage: get combo: %w", err)
	}
	return c, nil
}

func (s *sqliteCombos) Put(ctx context.Context, c Combo) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO combos (name, strategy, members, enabled)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   strategy = excluded.strategy,
		   members = excluded.members,
		   enabled = excluded.enabled`,
		c.Name, string(c.Strategy), encodeFallback(c.Members), c.Enabled)
	if err != nil {
		return fmt.Errorf("storage: put combo: %w", err)
	}
	return nil
}

func (s *sqliteCombos) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM combos WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("storage: delete combo: %w", err)
	}
	return affectOne(res, "delete combo")
}

type sqliteProxies struct {
	db *sql.DB
}

func (s *sqliteProxies) List(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, url, enabled FROM proxies ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("storage: list proxies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Proxy
	for rows.Next() {
		var p Proxy
		if err := rows.Scan(&p.Name, &p.URL, &p.Enabled); err != nil {
			return nil, fmt.Errorf("storage: scan proxy: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list proxies: %w", err)
	}
	return out, nil
}

func (s *sqliteProxies) Get(ctx context.Context, name string) (Proxy, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT name, url, enabled FROM proxies WHERE name = ?`, name)

	var p Proxy
	err := row.Scan(&p.Name, &p.URL, &p.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Proxy{}, ErrNotFound
	}
	if err != nil {
		return Proxy{}, fmt.Errorf("storage: get proxy: %w", err)
	}
	return p, nil
}

func (s *sqliteProxies) Put(ctx context.Context, p Proxy) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO proxies (name, url, enabled)
		 VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		   url = excluded.url,
		   enabled = excluded.enabled`,
		p.Name, p.URL, p.Enabled)
	if err != nil {
		return fmt.Errorf("storage: put proxy: %w", err)
	}
	return nil
}

func (s *sqliteProxies) Delete(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("storage: delete proxy: %w", err)
	}
	return affectOne(res, "delete proxy")
}

// pruneComboMembers strips deleted aliases from every combo pool and removes
// the combos left without a single member, so no combo routes into thin air.
func pruneComboMembers(ctx context.Context, tx *sql.Tx, dropped map[string]bool) error {
	if len(dropped) == 0 {
		return nil
	}

	rows, err := tx.QueryContext(ctx, `SELECT name, members FROM combos WHERE members <> ''`)
	if err != nil {
		return fmt.Errorf("storage: list combos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	updates := map[string]string{}
	for rows.Next() {
		var name, members string
		if err := rows.Scan(&name, &members); err != nil {
			return fmt.Errorf("storage: scan combo members: %w", err)
		}
		pool := decodeFallback(members)
		kept := make([]string, 0, len(pool))
		for _, member := range pool {
			if !dropped[member] {
				kept = append(kept, member)
			}
		}
		if len(kept) != len(pool) {
			updates[name] = encodeFallback(kept)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("storage: list combos: %w", err)
	}

	for name, members := range updates {
		if members == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM combos WHERE name = ?`, name); err != nil {
				return fmt.Errorf("storage: delete empty combo: %w", err)
			}
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE combos SET members = ? WHERE name = ?`, members, name); err != nil {
			return fmt.Errorf("storage: prune combo members: %w", err)
		}
	}
	return nil
}

func scanCombo(src scanner) (Combo, error) {
	var (
		c        Combo
		strategy string
		members  string
	)
	if err := src.Scan(&c.Name, &strategy, &members, &c.Enabled); err != nil {
		return Combo{}, err
	}
	c.Strategy = ComboStrategy(strategy)
	c.Members = decodeFallback(members)
	return c, nil
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

type sqliteUsage struct {
	db *sql.DB
}

const usageColumns = `id, created_at, key_id, alias, provider, model, streamed, status, duration_ms,
	prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens, reasoning_tokens, cost_usd`

func (s *sqliteUsage) Record(ctx context.Context, e UsageEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_events
		   (created_at, key_id, alias, provider, model, streamed, status, duration_ms,
		    prompt_tokens, completion_tokens, cached_tokens, cache_write_tokens,
		    reasoning_tokens, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.CreatedAt.Unix(), e.KeyID, e.Alias, e.Provider, e.Model, e.Streamed, e.Status,
		e.Duration.Milliseconds(), e.PromptTokens, e.CompletionTokens, e.CachedTokens,
		e.CacheWriteTokens, e.ReasoningTokens, e.CostUSD)
	if err != nil {
		return fmt.Errorf("storage: record usage: %w", err)
	}
	return nil
}

func (s *sqliteUsage) Recent(ctx context.Context, limit int) ([]UsageEvent, error) {
	if limit <= 0 {
		return nil, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT `+usageColumns+` FROM usage_events ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: recent usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UsageEvent
	for rows.Next() {
		e, err := scanUsageEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan usage event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: recent usage: %w", err)
	}
	return out, nil
}

func (s *sqliteUsage) Totals(ctx context.Context, since time.Time) (UsageTotals, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(status <> 'ok'), 0),
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM usage_events WHERE created_at >= ?`, since.Unix())

	var t UsageTotals
	if err := row.Scan(&t.Requests, &t.Errors, &t.PromptTokens,
		&t.CompletionTokens, &t.CachedTokens, &t.CostUSD); err != nil {
		return UsageTotals{}, fmt.Errorf("storage: usage totals: %w", err)
	}
	return t, nil
}

func (s *sqliteUsage) Series(ctx context.Context, since time.Time, bucket time.Duration) ([]UsageBucket, error) {
	seconds := int64(bucket.Seconds())
	if seconds <= 0 {
		return nil, nil
	}

	// Integer division floors each timestamp onto its bucket start.
	rows, err := s.db.QueryContext(ctx,
		`SELECT (created_at / ?) * ? AS start,
		        COUNT(*),
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0)
		 FROM usage_events
		 WHERE created_at >= ?
		 GROUP BY start
		 ORDER BY start`, seconds, seconds, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("storage: usage series: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UsageBucket
	for rows.Next() {
		var (
			start int64
			b     UsageBucket
		)
		if err := rows.Scan(&start, &b.Requests, &b.PromptTokens,
			&b.CompletionTokens, &b.CachedTokens, &b.CostUSD); err != nil {
			return nil, fmt.Errorf("storage: scan usage bucket: %w", err)
		}
		b.Start = time.Unix(start, 0).UTC()
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: usage series: %w", err)
	}
	return out, nil
}

func (s *sqliteUsage) ByModel(ctx context.Context) ([]ModelUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT alias,
		        COUNT(*),
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0),
		        MAX(created_at)
		 FROM usage_events
		 GROUP BY alias
		 ORDER BY MAX(created_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("storage: usage by model: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ModelUsage
	for rows.Next() {
		var (
			m        ModelUsage
			lastUsed int64
		)
		if err := rows.Scan(&m.Alias, &m.Requests, &m.PromptTokens,
			&m.CompletionTokens, &m.CachedTokens, &m.CostUSD, &lastUsed); err != nil {
			return nil, fmt.Errorf("storage: scan model usage: %w", err)
		}
		m.LastUsed = time.Unix(lastUsed, 0).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: usage by model: %w", err)
	}
	return out, nil
}

func (s *sqliteUsage) Leaderboard(ctx context.Context, since time.Time, limit int, sortBy LeaderboardSort) ([]ModelUsage, error) {
	if limit <= 0 {
		limit = 10
	}
	// Sort expression is chosen from a fixed set; never user input directly.
	order := "COUNT(*) DESC"
	switch sortBy {
	case LeaderboardByTokens:
		order = "COALESCE(SUM(prompt_tokens), 0) + COALESCE(SUM(completion_tokens), 0) DESC"
	case LeaderboardByCost:
		order = "COALESCE(SUM(cost_usd), 0) DESC"
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT alias,
		        COUNT(*),
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0),
		        MAX(created_at)
		 FROM usage_events
		 WHERE created_at >= ?
		 GROUP BY alias
		 ORDER BY `+order+`
		 LIMIT ?`, since.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("storage: usage leaderboard: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []ModelUsage
	for rows.Next() {
		var (
			m        ModelUsage
			lastUsed int64
		)
		if err := rows.Scan(&m.Alias, &m.Requests, &m.PromptTokens,
			&m.CompletionTokens, &m.CachedTokens, &m.CostUSD, &lastUsed); err != nil {
			return nil, fmt.Errorf("storage: scan usage leaderboard: %w", err)
		}
		m.LastUsed = time.Unix(lastUsed, 0).UTC()
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: usage leaderboard: %w", err)
	}
	return out, nil
}

func (s *sqliteUsage) ByKey(ctx context.Context, since time.Time) ([]KeyUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key_id,
		        COUNT(*),
		        COALESCE(SUM(prompt_tokens), 0),
		        COALESCE(SUM(completion_tokens), 0),
		        COALESCE(SUM(cached_tokens), 0),
		        COALESCE(SUM(cost_usd), 0),
		        MAX(created_at)
		 FROM usage_events
		 WHERE created_at >= ?
		 GROUP BY key_id
		 ORDER BY COALESCE(SUM(cost_usd), 0) DESC`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("storage: usage by key: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []KeyUsage
	for rows.Next() {
		var (
			k        KeyUsage
			lastUsed int64
		)
		if err := rows.Scan(&k.KeyID, &k.Requests, &k.PromptTokens,
			&k.CompletionTokens, &k.CachedTokens, &k.CostUSD, &lastUsed); err != nil {
			return nil, fmt.Errorf("storage: scan key usage: %w", err)
		}
		k.LastUsed = time.Unix(lastUsed, 0).UTC()
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: usage by key: %w", err)
	}
	return out, nil
}

func (s *sqliteUsage) KeySpend(ctx context.Context, keyID string, since time.Time) (float64, error) {
	var total float64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost_usd), 0) FROM usage_events
		 WHERE key_id = ? AND created_at >= ?`, keyID, since.Unix()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("storage: key spend: %w", err)
	}
	return total, nil
}

func (s *sqliteUsage) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM usage_events WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0, fmt.Errorf("storage: prune usage: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: prune usage: %w", err)
	}
	return removed, nil
}

func scanUsageEvent(src scanner) (UsageEvent, error) {
	var (
		e          UsageEvent
		createdAt  int64
		durationMS int64
	)
	if err := src.Scan(&e.ID, &createdAt, &e.KeyID, &e.Alias, &e.Provider, &e.Model,
		&e.Streamed, &e.Status, &durationMS, &e.PromptTokens, &e.CompletionTokens,
		&e.CachedTokens, &e.CacheWriteTokens, &e.ReasoningTokens, &e.CostUSD); err != nil {
		return UsageEvent{}, err
	}
	e.CreatedAt = time.Unix(createdAt, 0).UTC()
	e.Duration = time.Duration(durationMS) * time.Millisecond
	return e, nil
}
