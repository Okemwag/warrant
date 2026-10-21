// Package ledger stores permissions, decisions and budget reservations in
// Postgres. Every change to a budget locks that month's budget_periods row
// with SELECT ... FOR UPDATE, so concurrent purchases can never overspend.
package ledger

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/okemwag/warrant/engine"
)

// Errors returned by the ledger. The API maps them to HTTP statuses.
var (
	ErrNotFound    = errors.New("ledger: not found")
	ErrConflict    = errors.New("ledger: state conflict")
	ErrInvalid     = errors.New("ledger: invalid input")
	ErrExpired     = errors.New("ledger: reservation expired")
	ErrNotHeld     = errors.New("ledger: reservation is not held")
	ErrRevoked     = errors.New("ledger: permission is revoked")
	errLockTimeout = errors.New("ledger: budget lock timeout")
)

// Signer signs permission versions.
type Signer interface {
	Sign(engine.Permission) []byte
	PublicKey() ed25519.PublicKey
	KeyID() string
}

// Config tunes the ledger.
type Config struct {
	ReservationTTL time.Duration // how long a hold lasts; default 15m
	LockTimeout    time.Duration // wait for the budget lock before BUDGET_CONFLICT; default 2s
	Now            func() time.Time
}

// Ledger is the Postgres-backed store.
type Ledger struct {
	pool   *pgxpool.Pool
	signer Signer
	ttl    time.Duration
	lockMS int64
	now    func() time.Time
}

// New returns a Ledger.
func New(pool *pgxpool.Pool, s Signer, cfg Config) *Ledger {
	if cfg.ReservationTTL <= 0 {
		cfg.ReservationTTL = 15 * time.Minute
	}
	if cfg.LockTimeout <= 0 {
		cfg.LockTimeout = 2 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Ledger{pool: pool, signer: s, ttl: cfg.ReservationTTL, lockMS: cfg.LockTimeout.Milliseconds(), now: cfg.Now}
}

// PublicKey is the key permissions are verified against.
func (l *Ledger) PublicKey() ed25519.PublicKey { return l.signer.PublicKey() }

// ReservationTTL is how long holds last.
func (l *Ledger) ReservationTTL() time.Duration { return l.ttl }

func (l *Ledger) clock() time.Time { return l.now().UTC() }

// Period is the budget month for t, e.g. "2026-10".
func Period(t time.Time) string { return t.UTC().Format("2006-01") }

// inTx runs fn in a transaction with a lock timeout.
func (l *Ledger) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", itoa(l.lockMS)+"ms"); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// lockBudget creates the month's budget row if needed and locks it.
func lockBudget(ctx context.Context, tx pgx.Tx, permissionID, period string) (engine.Budget, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO budget_periods (permission_id, period) VALUES ($1, $2) ON CONFLICT DO NOTHING`, permissionID, period); err != nil {
		return engine.Budget{}, mapLockErr(err)
	}
	var b engine.Budget
	err := tx.QueryRow(ctx, `SELECT committed_cents, held_cents FROM budget_periods
		WHERE permission_id = $1 AND period = $2 FOR UPDATE`, permissionID, period).Scan(&b.Committed, &b.Held)
	return b, mapLockErr(err)
}

func addEvent(ctx context.Context, tx pgx.Tx, at time.Time, permissionID, decisionID, typ string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var dec *string
	if decisionID != "" {
		dec = &decisionID
	}
	_, err = tx.Exec(ctx, `INSERT INTO events (permission_id, decision_id, type, data, at) VALUES ($1, $2, $3, $4, $5)`,
		permissionID, dec, typ, b, at)
	return err
}

func mapLockErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "55P03" || pgErr.Code == "40P01") { // lock_not_available, deadlock_detected
		return errLockTimeout
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
