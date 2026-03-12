// Package repository provides PostgreSQL-backed implementations of port interfaces.
// The only external dependency is github.com/lib/pq (database/sql driver).
// No ORM — raw SQL keeps the dependency graph minimal and query behaviour explicit.
package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq" // registers the "postgres" driver with database/sql

	"github.com/rechedev9/carrier-gateway/internal/domain"
	"github.com/rechedev9/carrier-gateway/internal/ports"
)

// migrateSQL contains the idempotent DDL to create the quotes table.
// Using CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS means this can
// be called safely on every startup without a migration-version table.
const migrateSQL = `
CREATE TABLE IF NOT EXISTS quotes (
    request_id    TEXT        NOT NULL,
    carrier_id    TEXT        NOT NULL,
    premium_cents BIGINT      NOT NULL,
    currency      TEXT        NOT NULL DEFAULT 'USD',
    expires_at    TIMESTAMPTZ NOT NULL,
    is_hedged     BOOLEAN     NOT NULL DEFAULT FALSE,
    latency_ms    BIGINT      NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (request_id, carrier_id)
);
CREATE INDEX IF NOT EXISTS quotes_expires_at_idx ON quotes (expires_at);
`

// PostgresRepo implements ports.QuoteRepository using database/sql + lib/pq.
type PostgresRepo struct {
	db *sql.DB
}

// Open dials the database at dsn and verifies connectivity with a Ping.
// dsn format: "postgres://user:pass@host:5432/dbname?sslmode=disable"
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("repository: open postgres: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("repository: ping postgres: %w", err)
	}
	return db, nil
}

// New wraps an already-open *sql.DB. Callers are responsible for closing the
// database when done.
func New(db *sql.DB) *PostgresRepo {
	return &PostgresRepo{db: db}
}

// Migrate runs the idempotent DDL to create required tables and indexes.
// Safe to call on every application start.
func (r *PostgresRepo) Migrate(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, migrateSQL); err != nil {
		return fmt.Errorf("repository: migrate: %w", err)
	}
	return nil
}

// Save persists all results for a fan-out in a single transaction.
// Duplicate (request_id, carrier_id) pairs are silently ignored — this makes
// Save idempotent so re-submitting the same request_id never causes an error.
func (r *PostgresRepo) Save(ctx context.Context, requestID string, results []domain.QuoteResult) error {
	const insertSQL = `
		INSERT INTO quotes (request_id, carrier_id, premium_cents, currency, expires_at, is_hedged, latency_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (request_id, carrier_id) DO NOTHING
	`
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("repository: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback on error paths is intentional

	for _, res := range results {
		currency := res.Premium.Currency
		if currency == "" {
			currency = "USD"
		}
		if _, err := tx.ExecContext(ctx, insertSQL,
			requestID,
			res.CarrierID,
			res.Premium.Amount,
			currency,
			res.ExpiresAt,
			res.IsHedged,
			res.Latency.Milliseconds(),
		); err != nil {
			return fmt.Errorf("repository: insert quote for carrier %q: %w", res.CarrierID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("repository: commit: %w", err)
	}
	return nil
}

// FindByRequestID returns non-expired quote rows for requestID, sorted by
// premium ascending. ok is false when no rows exist or all have expired.
func (r *PostgresRepo) FindByRequestID(ctx context.Context, requestID string) ([]domain.QuoteResult, bool, error) {
	const querySQL = `
		SELECT carrier_id, premium_cents, currency, expires_at, is_hedged, latency_ms
		FROM quotes
		WHERE request_id = $1 AND expires_at > NOW()
		ORDER BY premium_cents ASC
	`
	rows, err := r.db.QueryContext(ctx, querySQL, requestID)
	if err != nil {
		return nil, false, fmt.Errorf("repository: query quotes: %w", err)
	}
	defer rows.Close()

	var results []domain.QuoteResult
	for rows.Next() {
		var (
			carrierID    string
			premiumCents int64
			currency     string
			expiresAt    time.Time
			isHedged     bool
			latencyMs    int64
		)
		if err := rows.Scan(&carrierID, &premiumCents, &currency, &expiresAt, &isHedged, &latencyMs); err != nil {
			return nil, false, fmt.Errorf("repository: scan quote row: %w", err)
		}
		results = append(results, domain.QuoteResult{
			RequestID: requestID,
			CarrierID: carrierID,
			Premium: domain.Money{
				Amount:   premiumCents,
				Currency: currency,
			},
			ExpiresAt: expiresAt,
			IsHedged:  isHedged,
			Latency:   time.Duration(latencyMs) * time.Millisecond,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("repository: rows iteration: %w", err)
	}
	return results, len(results) > 0, nil
}

// DeleteExpired removes all rows whose expires_at is in the past.
// Intended to be called periodically (e.g., via a background goroutine or cron).
func (r *PostgresRepo) DeleteExpired(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM quotes WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, fmt.Errorf("repository: delete expired: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Compile-time assertion that *PostgresRepo satisfies ports.QuoteRepository.
var _ ports.QuoteRepository = (*PostgresRepo)(nil)
