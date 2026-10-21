package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/ids"
)

// Reevaluate replaces a decision whose final checkout amount differs from
// what was approved. The old hold is released and the purchase is decided
// again at the new amount, with PRICE_DRIFT added so it always needs the
// person's approval (or is blocked if it now breaks a limit). Calling it
// twice for the same decision and amount returns the same new decision.
func (l *Ledger) Reevaluate(ctx context.Context, oldDecisionID string, newAmount engine.Cents) (*DecisionRecord, error) {
	old, err := l.GetDecision(ctx, oldDecisionID)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%s:drift:%d", old.ID, newAmount)
	if prev, err := decisionByKey(ctx, l.pool, old.PermissionID, key); err == nil {
		prev.Replayed = true
		return prev, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	now := l.clock()
	period := Period(now)
	prop := old.Proposal
	prop.Amount = newAmount
	var rec *DecisionRecord
	err = l.inTx(ctx, func(tx pgx.Tx) error {
		if old.Reservation != nil {
			res, err := lockReservation(ctx, tx, old.Reservation.ID)
			if err != nil {
				return err
			}
			if res.State == StateHeld {
				if err := releaseLocked(ctx, tx, res, ReleasePriceDrift, now); err != nil {
					return err
				}
			}
		}
		budget, err := lockBudget(ctx, tx, prop.PermissionID, period)
		if err != nil {
			return err
		}
		sp, version, err := loadSigned(ctx, tx, prop.PermissionID)
		if err != nil {
			return err
		}
		in := engine.Input{Permission: sp, PublicKey: l.signer.PublicKey(), Budget: budget, Proposal: prop, Now: now,
			Extra: []engine.Reason{engine.PriceDrift}}
		d, err := engine.Decide(in)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		explanation := engine.Explain(in, d)
		if engine.Primary(d.Reasons) == engine.PriceDrift {
			explanation = engine.ExplainPriceDrift(old.Amount, newAmount)
		}
		supersedes := old.ID
		rec = &DecisionRecord{
			ID: ids.New("dec"), PermissionID: prop.PermissionID, PermissionVersion: version, Proposal: prop,
			Outcome: d.Outcome, Reasons: d.Reasons, Explanation: explanation, Amount: newAmount,
			Remaining: d.RemainingAfter, CreatedAt: now, Supersedes: &supersedes,
		}
		if err := insertDecision(ctx, tx, rec, &key); err != nil {
			return err
		}
		if d.Reserve > 0 {
			rec.Reservation = &Reservation{
				ID: ids.New("res"), DecisionID: rec.ID, PermissionID: prop.PermissionID, Period: period,
				Amount: d.Reserve, State: StateHeld, ExpiresAt: now.Add(l.ttl),
			}
			if err := insertReservation(ctx, tx, rec.Reservation, now); err != nil {
				return err
			}
		}
		return addEvent(ctx, tx, now, prop.PermissionID, rec.ID, "decision", map[string]any{
			"decision": d.Outcome, "reasons": d.Reasons, "amount_cents": newAmount, "merchant": prop.Merchant,
			"supersedes": old.ID, "quoted_cents": old.Amount,
		})
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// ExtendHold restarts a held reservation's timer, e.g. when checkout starts.
func (l *Ledger) ExtendHold(ctx context.Context, reservationID string) error {
	now := l.clock()
	return l.inTx(ctx, func(tx pgx.Tx) error {
		res, err := lockReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		if res.State != StateHeld {
			return ErrNotHeld
		}
		if !now.Before(res.ExpiresAt) {
			return ErrExpired
		}
		_, err = tx.Exec(ctx, `UPDATE reservations SET expires_at = $2, updated_at = $3 WHERE id = $1`, res.ID, now.Add(l.ttl), now)
		return err
	})
}

// Refund moves committed spend back out of the budget after PayPal refunds
// the capture. Refunding twice is a no-op.
func (l *Ledger) Refund(ctx context.Context, reservationID string) error {
	now := l.clock()
	return l.inTx(ctx, func(tx pgx.Tx) error {
		res, err := lockReservation(ctx, tx, reservationID)
		if err != nil {
			return err
		}
		switch res.State {
		case StateRefunded:
			return nil
		case StateCommitted:
		default:
			return fmt.Errorf("%w: only completed purchases can be refunded", ErrConflict)
		}
		if _, err := tx.Exec(ctx, `UPDATE budget_periods SET committed_cents = committed_cents - $3
			WHERE permission_id = $1 AND period = $2`, res.PermissionID, res.Period, res.Amount); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE reservations SET state = 'refunded', updated_at = $2 WHERE id = $1`, res.ID, now); err != nil {
			return err
		}
		return addEvent(ctx, tx, now, res.PermissionID, res.DecisionID, "reservation.refunded", map[string]any{"amount_cents": res.Amount})
	})
}

func insertReservation(ctx context.Context, tx pgx.Tx, r *Reservation, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO reservations (id, decision_id, permission_id, period, amount_cents, state, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'held', $6, $7, $7)`,
		r.ID, r.DecisionID, r.PermissionID, r.Period, r.Amount, r.ExpiresAt, now); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE budget_periods SET held_cents = held_cents + $3 WHERE permission_id = $1 AND period = $2`,
		r.PermissionID, r.Period, r.Amount)
	return err
}
