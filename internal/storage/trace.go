package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxTraceError bounds how much of an upstream error message a trace keeps.
// The message is diagnostic, not a payload, and an upstream is free to echo
// arbitrary text back at us.
const maxTraceError = 512

type sqliteTraces struct {
	db *sql.DB
}

const traceColumns = `request_id, created_at, key_id, model, dialect, streamed, total_latency_ms,
	total_tokens, total_cost_usd, final_provider, final_alias, final_status, attempts`

const attemptColumns = `seq, started_at, alias, provider, model, latency_ms, status,
	retry, fallback, error`

// Record writes the summary and its attempts in one transaction. A repeat of
// the same request id replaces the previous trace, so a retried write after a
// partial failure converges rather than duplicating.
func (s *sqliteTraces) Record(ctx context.Context, t RequestTrace) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("storage: begin trace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO request_traces
		   (request_id, created_at, key_id, model, dialect, streamed, total_latency_ms,
		    total_tokens, total_cost_usd, final_provider, final_alias, final_status, attempts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (request_id) DO UPDATE SET
		   created_at = excluded.created_at,
		   key_id = excluded.key_id,
		   model = excluded.model,
		   dialect = excluded.dialect,
		   streamed = excluded.streamed,
		   total_latency_ms = excluded.total_latency_ms,
		   total_tokens = excluded.total_tokens,
		   total_cost_usd = excluded.total_cost_usd,
		   final_provider = excluded.final_provider,
		   final_alias = excluded.final_alias,
		   final_status = excluded.final_status,
		   attempts = excluded.attempts`,
		t.RequestID, t.CreatedAt.Unix(), t.KeyID, t.Model, dialectOrDefault(t.Dialect),
		t.Streamed, t.TotalLatency.Milliseconds(), t.TotalTokens, t.TotalCostUSD,
		t.FinalProvider, t.FinalAlias, t.FinalStatus, len(t.Attempts)); err != nil {
		return fmt.Errorf("storage: record trace: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM request_attempts WHERE request_id = ?`, t.RequestID); err != nil {
		return fmt.Errorf("storage: reset trace attempts: %w", err)
	}

	for _, a := range t.Attempts {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO request_attempts
			   (request_id, seq, started_at, alias, provider, model, latency_ms,
			    status, retry, fallback, error)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.RequestID, a.Seq, a.StartedAt.Unix(), a.Alias, a.Provider, a.Model,
			a.Latency.Milliseconds(), a.Status, a.Retry, a.Fallback,
			truncateError(a.Error)); err != nil {
			return fmt.Errorf("storage: record trace attempt: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("storage: commit trace: %w", err)
	}
	return nil
}

func (s *sqliteTraces) List(ctx context.Context, f TraceFilter) ([]RequestTrace, int, error) {
	where, args := traceWhere(f)

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM request_traces`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("storage: count traces: %w", err)
	}
	if total == 0 {
		return nil, 0, nil
	}

	limit := f.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+traceColumns+` FROM request_traces`+where+
			` ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`,
		append(args, limit, max(f.Offset, 0))...)
	if err != nil {
		return nil, 0, fmt.Errorf("storage: list traces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RequestTrace
	for rows.Next() {
		t, err := scanTrace(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("storage: scan trace: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("storage: list traces: %w", err)
	}
	return out, total, nil
}

// traceWhere builds the filter clause shared by the count and the page, so the
// two can never drift apart.
func traceWhere(f TraceFilter) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if f.Status != "" {
		clauses = append(clauses, "final_status = ?")
		args = append(args, f.Status)
	}
	if f.Model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, f.Model)
	}
	if f.Dialect != "" {
		clauses = append(clauses, "dialect = ?")
		args = append(args, f.Dialect)
	}
	if f.KeyID != "" {
		clauses = append(clauses, "key_id = ?")
		args = append(args, f.KeyID)
	}
	if f.Provider != "" {
		// Any attempt on the provider counts: a request that fell back off it
		// is still a request about it.
		clauses = append(clauses,
			`EXISTS (SELECT 1 FROM request_attempts a
			          WHERE a.request_id = request_traces.request_id AND a.provider = ?)`)
		args = append(args, f.Provider)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *sqliteTraces) Get(ctx context.Context, requestID string) (RequestTrace, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+traceColumns+` FROM request_traces WHERE request_id = ?`, requestID)

	t, err := scanTrace(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestTrace{}, ErrNotFound
	}
	if err != nil {
		return RequestTrace{}, fmt.Errorf("storage: get trace: %w", err)
	}

	t.Attempts, err = s.attempts(ctx, requestID)
	if err != nil {
		return RequestTrace{}, err
	}
	return t, nil
}

func (s *sqliteTraces) attempts(ctx context.Context, requestID string) ([]RequestAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+attemptColumns+` FROM request_attempts WHERE request_id = ? ORDER BY seq`,
		requestID)
	if err != nil {
		return nil, fmt.Errorf("storage: trace attempts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RequestAttempt
	for rows.Next() {
		var (
			a         RequestAttempt
			startedAt int64
			latencyMS int64
		)
		if err := rows.Scan(&a.Seq, &startedAt, &a.Alias, &a.Provider, &a.Model,
			&latencyMS, &a.Status, &a.Retry, &a.Fallback, &a.Error); err != nil {
			return nil, fmt.Errorf("storage: scan trace attempt: %w", err)
		}
		a.StartedAt = time.Unix(startedAt, 0).UTC()
		a.Latency = time.Duration(latencyMS) * time.Millisecond
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: trace attempts: %w", err)
	}
	return out, nil
}

// Prune deletes traces past the retention cutoff. Attempts go with them
// through the foreign key's cascade.
func (s *sqliteTraces) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM request_traces WHERE created_at < ?`, before.Unix())
	if err != nil {
		return 0, fmt.Errorf("storage: prune traces: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: prune traces: %w", err)
	}
	return removed, nil
}

func scanTrace(src scanner) (RequestTrace, error) {
	var (
		t         RequestTrace
		createdAt int64
		latencyMS int64
	)
	if err := src.Scan(&t.RequestID, &createdAt, &t.KeyID, &t.Model, &t.Dialect,
		&t.Streamed, &latencyMS, &t.TotalTokens, &t.TotalCostUSD, &t.FinalProvider,
		&t.FinalAlias, &t.FinalStatus, &t.AttemptCount); err != nil {
		return RequestTrace{}, err
	}
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.TotalLatency = time.Duration(latencyMS) * time.Millisecond
	return t, nil
}

func truncateError(msg string) string {
	if len(msg) <= maxTraceError {
		return msg
	}
	return msg[:maxTraceError]
}

// dialectOrDefault keeps the column non-empty. A trace recorded without a
// dialect predates the Anthropic endpoint, so OpenAI is the honest default.
func dialectOrDefault(d string) string {
	if d == "" {
		return DialectOpenAI
	}
	return d
}
