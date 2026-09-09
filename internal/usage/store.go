package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store interface {
	Insert(ctx context.Context, rec Record) error
	Close() error
	Overview(ctx context.Context, since time.Time) (Aggregate, error)
	ByModel(ctx context.Context, since time.Time) ([]ModelAggregate, error)
	ByProvider(ctx context.Context, since time.Time) ([]ProviderAggregate, error)
}

type dbStore struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS requests (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_id TEXT NOT NULL UNIQUE,
  ts INTEGER NOT NULL,
  model TEXT NOT NULL,
  protocol TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  usage_kind TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);
CREATE TABLE IF NOT EXISTS attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  request_row INTEGER NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  outcome TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cached_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  UNIQUE(request_row, ordinal)
);
CREATE INDEX IF NOT EXISTS idx_attempts_provider ON attempts(provider);
`

func Open(path string) (Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("usage: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("usage: apply schema: %w", err)
	}
	return &dbStore{db: db}, nil
}

func (s *dbStore) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("usage: close: %w", err)
	}
	return nil
}

func (s *dbStore) Insert(ctx context.Context, rec Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("usage: begin: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO requests
  (request_id, ts, model, protocol, status, reason, usage_kind,
   input_tokens, output_tokens, cached_tokens, reasoning_tokens, total_tokens, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RequestID, rec.Timestamp.UnixNano(), rec.Model, rec.Protocol, rec.Status, rec.Reason,
		rec.UsageKind.String(),
		rec.Usage.InputTokens, rec.Usage.OutputTokens, rec.Usage.CachedInputTokens,
		rec.Usage.ReasoningTokens, rec.Usage.TotalTokens, rec.Duration.Milliseconds())
	if err != nil {
		return fmt.Errorf("usage: insert request: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("usage: insert request: %w", err)
	}
	if n == 0 {
		return tx.Commit()
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("usage: insert request: %w", err)
	}
	for i, at := range rec.Attempts {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO attempts
  (request_row, ordinal, provider, model, outcome,
   input_tokens, output_tokens, cached_tokens, reasoning_tokens, total_tokens)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, i+1, at.Provider, at.Model, at.Outcome,
			at.Usage.InputTokens, at.Usage.OutputTokens, at.Usage.CachedInputTokens,
			at.Usage.ReasoningTokens, at.Usage.TotalTokens); err != nil {
			return fmt.Errorf("usage: insert attempt: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("usage: commit: %w", err)
	}
	return nil
}

const requestAggregates = `
COUNT(*),
COUNT(CASE WHEN r.status = 'completed' THEN 1 END),
COUNT(CASE WHEN r.status = 'failed' THEN 1 END),
COALESCE(SUM(r.input_tokens), 0),
COALESCE(SUM(r.output_tokens), 0),
COALESCE(SUM(r.cached_tokens), 0),
COALESCE(SUM(r.reasoning_tokens), 0),
COALESCE(SUM(r.total_tokens), 0),
COUNT(CASE WHEN r.usage_kind = 'reported' THEN 1 END)`

func (s *dbStore) Overview(ctx context.Context, since time.Time) (Aggregate, error) {
	var a Aggregate
	err := s.db.QueryRowContext(ctx,
		"SELECT "+requestAggregates+" FROM requests r WHERE r.ts >= ?", since.UnixNano()).
		Scan(&a.Requests, &a.Completed, &a.Failed, &a.InputTokens, &a.OutputTokens,
			&a.CachedTokens, &a.ReasoningTokens, &a.TotalTokens, &a.Measured)
	if err != nil {
		return a, fmt.Errorf("usage: overview: %w", err)
	}
	return a, nil
}

func (s *dbStore) ByModel(ctx context.Context, since time.Time) ([]ModelAggregate, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT r.model, "+requestAggregates+" FROM requests r WHERE r.ts >= ? GROUP BY r.model ORDER BY COALESCE(SUM(r.total_tokens), 0) DESC",
		since.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("usage: by model: %w", err)
	}
	defer rows.Close()

	var out []ModelAggregate
	for rows.Next() {
		var m ModelAggregate
		if err := rows.Scan(&m.Model, &m.Requests, &m.Completed, &m.Failed, &m.InputTokens,
			&m.OutputTokens, &m.CachedTokens, &m.ReasoningTokens, &m.TotalTokens, &m.Measured); err != nil {
			return nil, fmt.Errorf("usage: by model: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage: by model: %w", err)
	}
	terminal, err := s.terminalProviders(ctx, since)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Provider = terminal[out[i].Model]
	}
	return out, nil
}

// terminalProviders maps each model to the provider of the last attempt of its
// most recent request; ordering by ts makes later rows win.
func (s *dbStore) terminalProviders(ctx context.Context, since time.Time) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.model, a.provider
FROM attempts a
JOIN requests r ON r.id = a.request_row
WHERE r.ts >= ?
  AND a.ordinal = (SELECT MAX(a2.ordinal) FROM attempts a2 WHERE a2.request_row = a.request_row)
ORDER BY r.ts`, since.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("usage: terminal providers: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var model, provider string
		if err := rows.Scan(&model, &provider); err != nil {
			return nil, fmt.Errorf("usage: terminal providers: %w", err)
		}
		out[model] = provider
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage: terminal providers: %w", err)
	}
	return out, nil
}

// ByProvider attributes each request to the provider of its terminal attempt and
// sums only that attempt's tokens, so rows stay request-level and add up to Overview.
func (s *dbStore) ByProvider(ctx context.Context, since time.Time) ([]ProviderAggregate, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT a.provider, `+requestAggregates+`
FROM requests r
JOIN attempts a ON a.request_row = r.id
  AND a.ordinal = (SELECT MAX(a2.ordinal) FROM attempts a2 WHERE a2.request_row = r.id)
WHERE r.ts >= ?
GROUP BY a.provider
ORDER BY COALESCE(SUM(a.total_tokens), 0) DESC`, since.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("usage: by provider: %w", err)
	}
	defer rows.Close()

	var out []ProviderAggregate
	for rows.Next() {
		var p ProviderAggregate
		if err := rows.Scan(&p.Provider, &p.Requests, &p.Completed, &p.Failed, &p.InputTokens,
			&p.OutputTokens, &p.CachedTokens, &p.ReasoningTokens, &p.TotalTokens, &p.Measured); err != nil {
			return nil, fmt.Errorf("usage: by provider: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage: by provider: %w", err)
	}
	return out, nil
}
