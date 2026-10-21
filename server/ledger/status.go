package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/okemwag/warrant/engine"
)

// SetStatus pauses, resumes or revokes a permission. Pausing and revoking
// release every open hold in the same transaction.
func (l *Ledger) SetStatus(ctx context.Context, id string, to engine.Status) (*PermissionView, error) {
	now := l.clock()
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		// Lock every budget month for this permission first (same order as
		// Evaluate: budget rows, then the rest), so no evaluation can slip a
		// new hold in while we pause.
		if _, err := tx.Exec(ctx, `INSERT INTO budget_periods (permission_id, period)
			SELECT id, $2 FROM permissions WHERE id = $1 ON CONFLICT DO NOTHING`, id, Period(now)); err != nil {
			return mapLockErr(err)
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM budget_periods WHERE permission_id = $1 ORDER BY period FOR UPDATE`, id); err != nil {
			return mapLockErr(err)
		}
		var from engine.Status
		var current *int
		err := tx.QueryRow(ctx, `SELECT status, current_version FROM permissions WHERE id = $1 FOR UPDATE`, id).Scan(&from, &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := checkTransition(from, to, current != nil); err != nil {
			return err
		}
		if from == to {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE permissions SET status = $2, updated_at = $3 WHERE id = $1`, id, to, now); err != nil {
			return err
		}
		if to == engine.StatusPaused || to == engine.StatusRevoked {
			reason := ReleasePaused
			if to == engine.StatusRevoked {
				reason = ReleaseRevoked
			}
			rows, err := tx.Query(ctx, reservationSelect+` WHERE permission_id = $1 AND state = 'held' FOR UPDATE`, id)
			if err != nil {
				return err
			}
			held, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Reservation, error) { return scanReservation(r) })
			if err != nil {
				return err
			}
			for _, res := range held {
				if err := releaseLocked(ctx, tx, res, reason, now); err != nil {
					return err
				}
			}
		}
		return addEvent(ctx, tx, now, id, "", "permission."+string(to), map[string]any{"from": from})
	})
	if err != nil {
		return nil, err
	}
	return l.GetPermission(ctx, id)
}

func checkTransition(from, to engine.Status, signed bool) error {
	switch {
	case from == engine.StatusRevoked:
		return ErrRevoked
	case to == engine.StatusRevoked:
		return nil
	case to == engine.StatusPaused && (from == engine.StatusActive || from == engine.StatusPaused):
		return nil
	case to == engine.StatusActive && (from == engine.StatusPaused || from == engine.StatusActive) && signed:
		return nil
	case to == engine.StatusActive && from == engine.StatusDraft:
		return fmt.Errorf("%w: sign the permission to activate it", ErrConflict)
	default:
		return fmt.Errorf("%w: cannot change status from %s to %s", ErrConflict, from, to)
	}
}
