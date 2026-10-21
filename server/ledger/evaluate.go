package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/ids"
)

// Reservation is a budget hold.
type Reservation struct {
	ID            string       `json:"id"`
	DecisionID    string       `json:"decision_id"`
	PermissionID  string       `json:"permission_id"`
	Period        string       `json:"period"`
	Amount        engine.Cents `json:"amount_cents"`
	State         string       `json:"state"`
	ReleaseReason *string      `json:"release_reason,omitempty"`
	ExpiresAt     time.Time    `json:"expires_at"`
}

// Reservation states.
const (
	StateHeld      = "held"
	StateCommitted = "committed"
	StateReleased  = "released"
	StateRefunded  = "refunded"
)

// DecisionRecord is a stored decision.
type DecisionRecord struct {
	ID                string          `json:"decision_id"`
	PermissionID      string          `json:"permission_id"`
	PermissionVersion *int            `json:"permission_version"`
	Proposal          engine.Proposal `json:"proposal"`
	Outcome           engine.Outcome  `json:"decision"`
	Reasons           []engine.Reason `json:"reasons"`
	Explanation       string          `json:"explanation"`
	Amount            engine.Cents    `json:"amount_cents"`
	Remaining         engine.Cents    `json:"remaining_after_cents"`
	HumanAction       *string         `json:"human_action,omitempty"`
	HumanAt           *time.Time      `json:"human_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	Supersedes        *string         `json:"supersedes,omitempty"`
	Reservation       *Reservation    `json:"reservation,omitempty"`
	// Replayed is true when an Idempotency-Key matched an earlier call.
	Replayed bool `json:"replayed,omitempty"`
}

// Status is the purchase's effective state for the agent: whether it may
// proceed to checkout.
func (d DecisionRecord) Status() string {
	switch {
	case d.Outcome == engine.Block:
		return "blocked"
	case d.Reservation == nil:
		return "blocked"
	case d.Reservation.State == StateCommitted:
		return "completed"
	case d.Reservation.State != StateHeld:
		return "released"
	case d.Outcome == engine.Allow:
		return "ready"
	case d.HumanAction != nil && *d.HumanAction == "approved":
		return "ready"
	default:
		return "awaiting_approval"
	}
}

// Evaluate decides a proposal and, unless blocked, holds its amount against
// this month's budget, atomically. An idempotencyKey that was used before for
// the same permission returns the original decision.
func (l *Ledger) Evaluate(ctx context.Context, idempotencyKey string, prop engine.Proposal) (*DecisionRecord, error) {
	if idempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency key is required", ErrInvalid)
	}
	prop = prop.Normalize()
	if err := prop.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	var exists bool
	if err := l.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM permissions WHERE id = $1)`, prop.PermissionID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}

	now := l.clock()
	period := Period(now)
	var rec *DecisionRecord
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		budget, err := lockBudget(ctx, tx, prop.PermissionID, period)
		if err != nil {
			return err
		}
		// Check idempotency only after taking the lock, so two concurrent
		// calls with the same key cannot both reserve.
		if prev, err := decisionByKey(ctx, tx, prop.PermissionID, idempotencyKey); err == nil {
			prev.Replayed = true
			rec = prev
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}

		sp, version, err := loadSigned(ctx, tx, prop.PermissionID)
		if err != nil {
			return err
		}
		in := engine.Input{Permission: sp, PublicKey: l.signer.PublicKey(), Budget: budget, Proposal: prop, Now: now}
		d, err := engine.Decide(in)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		rec = &DecisionRecord{
			ID: ids.New("dec"), PermissionID: prop.PermissionID, PermissionVersion: version,
			Proposal: prop, Outcome: d.Outcome, Reasons: d.Reasons,
			Explanation: engine.Explain(in, d), Amount: prop.Amount, Remaining: d.RemainingAfter, CreatedAt: now,
		}
		if err := insertDecision(ctx, tx, rec, &idempotencyKey); err != nil {
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
			"decision": d.Outcome, "reasons": d.Reasons, "amount_cents": prop.Amount, "merchant": prop.Merchant,
		})
	})
	switch {
	case errors.Is(err, errLockTimeout):
		return l.recordConflict(ctx, prop, now)
	case err != nil && isUniqueViolation(err):
		// A concurrent call with the same key won; return its decision.
		prev, err := decisionByKey(ctx, l.pool, prop.PermissionID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		prev.Replayed = true
		return prev, nil
	case err != nil:
		return nil, err
	}
	return rec, nil
}

// recordConflict stores a BUDGET_CONFLICT decision without an idempotency
// key, so the agent can retry with the same key.
func (l *Ledger) recordConflict(ctx context.Context, prop engine.Proposal, now time.Time) (*DecisionRecord, error) {
	d := engine.Conflict()
	rec := &DecisionRecord{
		ID: ids.New("dec"), PermissionID: prop.PermissionID, Proposal: prop,
		Outcome: d.Outcome, Reasons: d.Reasons, Explanation: engine.Explain(engine.Input{}, d),
		Amount: prop.Amount, CreatedAt: now,
	}
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		if err := insertDecision(ctx, tx, rec, nil); err != nil {
			return err
		}
		return addEvent(ctx, tx, now, prop.PermissionID, rec.ID, "decision", map[string]any{"decision": d.Outcome, "reasons": d.Reasons})
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// loadSigned returns the version in force. A permission that was never
// signed is returned as a draft, which the engine blocks.
func loadSigned(ctx context.Context, tx pgx.Tx, id string) (engine.SignedPermission, *int, error) {
	var status engine.Status
	var current *int
	var latest int
	if err := tx.QueryRow(ctx, `SELECT status, current_version, latest_version FROM permissions WHERE id = $1`, id).
		Scan(&status, &current, &latest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return engine.SignedPermission{}, nil, ErrNotFound
		}
		return engine.SignedPermission{}, nil, err
	}
	if current == nil {
		v, err := loadVersion(ctx, tx, id, latest)
		if err != nil {
			return engine.SignedPermission{}, nil, err
		}
		return engine.SignedPermission{Permission: v.Permission, Status: engine.StatusDraft}, nil, nil
	}
	v, err := loadVersion(ctx, tx, id, *current)
	if err != nil {
		return engine.SignedPermission{}, nil, err
	}
	return engine.SignedPermission{Permission: v.Permission, Status: status, Signature: v.Signature}, current, nil
}

func insertDecision(ctx context.Context, tx pgx.Tx, r *DecisionRecord, key *string) error {
	prop, err := json.Marshal(r.Proposal)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO decisions (id, permission_id, permission_version, idempotency_key, proposal, outcome, reasons, explanation, amount_cents, remaining_cents, created_at, supersedes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.ID, r.PermissionID, r.PermissionVersion, key, prop, r.Outcome, reasonStrings(r.Reasons), r.Explanation, r.Amount, r.Remaining, r.CreatedAt, r.Supersedes)
	return err
}

func reasonStrings(rs []engine.Reason) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
