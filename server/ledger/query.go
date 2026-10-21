package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/okemwag/warrant/engine"
)

const decisionSelect = `SELECT id, permission_id, permission_version, proposal, outcome, reasons, explanation,
	amount_cents, remaining_cents, human_action, human_at, created_at, supersedes FROM decisions`

// GetDecision returns a decision with its reservation.
func (l *Ledger) GetDecision(ctx context.Context, id string) (*DecisionRecord, error) {
	rec, err := scanDecision(l.pool.QueryRow(ctx, decisionSelect+` WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	return rec, attachReservation(ctx, l.pool, rec)
}

// ListDecisions returns a permission's decisions, newest first.
func (l *Ledger) ListDecisions(ctx context.Context, permissionID string, limit int) ([]DecisionRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := l.pool.Query(ctx, decisionSelect+` WHERE permission_id = $1 ORDER BY created_at DESC, id LIMIT $2`, permissionID, limit)
	if err != nil {
		return nil, err
	}
	recs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecisionRecord, error) {
		rec, err := scanDecision(r)
		if err != nil {
			return DecisionRecord{}, err
		}
		return *rec, nil
	})
	if err != nil {
		return nil, err
	}
	for i := range recs {
		if err := attachReservation(ctx, l.pool, &recs[i]); err != nil {
			return nil, err
		}
	}
	return recs, nil
}

// Event is one entry in the activity timeline.
type Event struct {
	ID           int64           `json:"id"`
	PermissionID string          `json:"permission_id"`
	DecisionID   *string         `json:"decision_id,omitempty"`
	Type         string          `json:"type"`
	Data         json.RawMessage `json:"data"`
	At           time.Time       `json:"at"`
}

// ListEvents returns a permission's timeline, newest first.
func (l *Ledger) ListEvents(ctx context.Context, permissionID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := l.pool.Query(ctx, `SELECT id, permission_id, decision_id, type, data, at FROM events
		WHERE permission_id = $1 ORDER BY at DESC, id DESC LIMIT $2`, permissionID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Event])
}

func decisionByKey(ctx context.Context, q querier, permissionID, key string) (*DecisionRecord, error) {
	rec, err := scanDecision(q.QueryRow(ctx, decisionSelect+` WHERE permission_id = $1 AND idempotency_key = $2`, permissionID, key))
	if err != nil {
		return nil, err
	}
	return rec, attachReservation(ctx, q, rec)
}

func attachReservation(ctx context.Context, q querier, rec *DecisionRecord) error {
	res, err := scanReservation(q.QueryRow(ctx, reservationSelect+` WHERE decision_id = $1`, rec.ID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	rec.Reservation = res
	return nil
}

func scanDecision(row pgx.Row) (*DecisionRecord, error) {
	var r DecisionRecord
	var prop []byte
	var reasons []string
	err := row.Scan(&r.ID, &r.PermissionID, &r.PermissionVersion, &prop, &r.Outcome, &reasons, &r.Explanation,
		&r.Amount, &r.Remaining, &r.HumanAction, &r.HumanAt, &r.CreatedAt, &r.Supersedes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(prop, &r.Proposal); err != nil {
		return nil, err
	}
	r.Reasons = make([]engine.Reason, len(reasons))
	for i, s := range reasons {
		r.Reasons[i] = engine.Reason(s)
	}
	return &r, nil
}
