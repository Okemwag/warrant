package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/okemwag/warrant/engine"
)

// Release reasons.
const (
	ReleaseDeclined   = "declined"
	ReleaseExpired    = "expired"
	ReleasePaused     = "permission_paused"
	ReleaseRevoked    = "permission_revoked"
	ReleasePayFailed  = "payment_failed"
	ReleasePriceDrift = "price_drift"
)

// Approve records the person's answer to a CONFIRM decision. Approving keeps
// the hold and restarts its timer so checkout can finish; declining releases it.
func (l *Ledger) Approve(ctx context.Context, decisionID string, approve bool) (*DecisionRecord, error) {
	now := l.clock()
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		res, err := lockReservationByDecision(ctx, tx, decisionID)
		if err != nil {
			return err
		}
		var outcome engine.Outcome
		var human *string
		if err := tx.QueryRow(ctx, `SELECT outcome, human_action FROM decisions WHERE id = $1 FOR UPDATE`, decisionID).Scan(&outcome, &human); err != nil {
			return err
		}
		if outcome != engine.Confirm {
			return fmt.Errorf("%w: only CONFIRM decisions need approval (this one is %s)", ErrConflict, outcome)
		}
		if human != nil {
			return fmt.Errorf("%w: already %s", ErrConflict, *human)
		}
		if res.State != StateHeld {
			return ErrNotHeld
		}
		if !now.Before(res.ExpiresAt) {
			return ErrExpired
		}
		action := "declined"
		if approve {
			action = "approved"
			if _, err := tx.Exec(ctx, `UPDATE reservations SET expires_at = $2, updated_at = $3 WHERE id = $1`, res.ID, now.Add(l.ttl), now); err != nil {
				return err
			}
		} else if err := releaseLocked(ctx, tx, res, ReleaseDeclined, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE decisions SET human_action = $2, human_at = $3 WHERE id = $1`, decisionID, action, now); err != nil {
			return err
		}
		return addEvent(ctx, tx, now, res.PermissionID, decisionID, "decision."+action, map[string]any{"amount_cents": res.Amount})
	})
	if err != nil {
		return nil, err
	}
	return l.GetDecision(ctx, decisionID)
}

// Commit moves a hold to committed spend after PayPal confirms the capture.
// Committing an already committed reservation is a no-op. A released hold
// can still be committed: PayPal's confirmation is the truth about money.
func (l *Ledger) Commit(ctx context.Context, reservationID string) error {
	now := l.clock()
	return l.inTx(ctx, func(tx pgx.Tx) error {
		res, err := lockReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		event := "reservation.committed"
		heldDelta := res.Amount
		switch res.State {
		case StateCommitted:
			return nil
		case StateHeld:
		case StateReleased:
			// PayPal confirmed a capture after the hold lapsed. The money
			// moved, so the spend must count even if it overshoots the budget.
			event, heldDelta = "reservation.committed_after_release", 0
		default:
			return ErrNotHeld
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_periods SET held_cents = held_cents - $3, committed_cents = committed_cents + $4
			WHERE permission_id = $1 AND period = $2`, res.PermissionID, res.Period, heldDelta, res.Amount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservations SET state = 'committed', updated_at = $2 WHERE id = $1`, res.ID, now); err != nil {
			return err
		}
		return addEvent(ctx, tx, now, res.PermissionID, res.DecisionID, event, map[string]any{"amount_cents": res.Amount})
	})
}

// Release frees a hold. Releasing an already released reservation is a no-op;
// a committed one cannot be released (refund it instead).
func (l *Ledger) Release(ctx context.Context, reservationID, reason string) error {
	now := l.clock()
	return l.inTx(ctx, func(tx pgx.Tx) error {
		res, err := lockReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		switch res.State {
		case StateReleased:
			return nil
		case StateHeld:
			return releaseLocked(ctx, tx, res, reason, now)
		default:
			return ErrNotHeld
		}
	})
}

// ReleaseExpired releases every hold past its expiry and returns how many.
func (l *Ledger) ReleaseExpired(ctx context.Context) (int, error) {
	rows, err := l.pool.Query(ctx, `SELECT id FROM reservations WHERE state = 'held' AND expires_at <= $1 ORDER BY expires_at`, l.clock())
	if err != nil {
		return 0, err
	}
	expired, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range expired {
		err := l.inTx(ctx, func(tx pgx.Tx) error {
			res, err := lockReservation(ctx, tx, id)
			if err != nil {
				return err
			}
			// Re-check under the lock: it may have been approved or committed meanwhile.
			if res.State != StateHeld || l.clock().Before(res.ExpiresAt) {
				return nil
			}
			n++
			return releaseLocked(ctx, tx, res, ReleaseExpired, l.clock())
		})
		if err != nil && !errors.Is(err, errLockTimeout) {
			return n, err
		}
	}
	return n, nil
}

// releaseLocked releases a held reservation. The budget row must be locked.
func releaseLocked(ctx context.Context, tx pgx.Tx, res *Reservation, reason string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE budget_periods SET held_cents = held_cents - $3 WHERE permission_id = $1 AND period = $2`,
		res.PermissionID, res.Period, res.Amount); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservations SET state = 'released', release_reason = $2, updated_at = $3 WHERE id = $1`,
		res.ID, reason, now); err != nil {
		return err
	}
	return addEvent(ctx, tx, now, res.PermissionID, res.DecisionID, "reservation.released", map[string]any{"amount_cents": res.Amount, "reason": reason})
}

// lockReservation locks the reservation's budget row, then the reservation.
// Budget row first keeps lock order consistent with Evaluate.
func lockReservation(ctx context.Context, tx pgx.Tx, id string) (*Reservation, error) {
	var permID, period string
	err := tx.QueryRow(ctx, `SELECT permission_id, period FROM reservations WHERE id = $1`, id).Scan(&permID, &period)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := lockBudget(ctx, tx, permID, period); err != nil {
		return nil, err
	}
	return scanReservation(tx.QueryRow(ctx, reservationSelect+` WHERE id = $1 FOR UPDATE`, id))
}

func lockReservationByDecision(ctx context.Context, tx pgx.Tx, decisionID string) (*Reservation, error) {
	var resID string
	err := tx.QueryRow(ctx, `SELECT id FROM reservations WHERE decision_id = $1`, decisionID).Scan(&resID)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM decisions WHERE id = $1)`, decisionID).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: this decision holds no budget", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	return lockReservation(ctx, tx, resID)
}

const reservationSelect = `SELECT id, decision_id, permission_id, period, amount_cents, state, release_reason, expires_at FROM reservations`

func scanReservation(row pgx.Row) (*Reservation, error) {
	var r Reservation
	err := row.Scan(&r.ID, &r.DecisionID, &r.PermissionID, &r.Period, &r.Amount, &r.State, &r.ReleaseReason, &r.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}
