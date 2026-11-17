package usage

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"
)

// RenameProvider moves the usage history of provider from to provider to in
// one transaction: attempts by provider, and request model ids starting
// with a key of modelPrefixes, which is replaced by its value. Only requests
// recorded at or before through move: a later request that still named the
// old id failed to resolve and keeps the model string the client sent. It
// reports how many attempts moved; a repeated call moves none.
func (s *dbStore) RenameProvider(ctx context.Context, from, to string, modelPrefixes map[string]string, through time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("usage: rename provider: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE attempts SET provider = ? WHERE provider = ? AND request_row IN (SELECT id FROM requests WHERE ts <= ?)`,
		to, from, through.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("usage: rename provider attempts: %w", err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("usage: rename provider attempts: %w", err)
	}
	for _, old := range slices.Sorted(maps.Keys(modelPrefixes)) {
		// A half-open range over the prefix keeps the update on the model
		// index; LIKE would treat '_' in an id as a wildcard.
		if _, err := tx.ExecContext(ctx,
			`UPDATE requests SET model = ? || substr(model, ?) WHERE model >= ? AND model < ? AND ts <= ?`,
			modelPrefixes[old], len(old)+1, old, prefixEnd(old), through.UnixNano()); err != nil {
			return 0, fmt.Errorf("usage: rename provider models: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("usage: rename provider: %w", err)
	}
	return moved, nil
}

// CountProvider is how many attempts are recorded for provider.
func (s *dbStore) CountProvider(ctx context.Context, provider string) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE provider = ?`, provider).Scan(&n); err != nil {
		return 0, fmt.Errorf("usage: count provider: %w", err)
	}
	return n, nil
}

// prefixEnd is the smallest string greater than every string with prefix p.
func prefixEnd(p string) string {
	b := []byte(p)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1])
		}
	}
	return string(b) + "\xff"
}
