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

// PermissionInput is what a person edits. ID and Version are assigned here.
type PermissionInput struct {
	Name             string       `json:"name"`
	MonthlyBudget    engine.Cents `json:"monthly_budget_cents"`
	PerPurchaseCap   engine.Cents `json:"per_purchase_cap_cents"`
	ConfirmAbove     engine.Cents `json:"confirm_above_cents"`
	Categories       []string     `json:"categories"`
	Merchants        []string     `json:"merchants"`
	AskOnNewMerchant bool         `json:"ask_on_new_merchant"`
	MinReturnDays    int          `json:"min_return_days"`
	Currency         string       `json:"currency"`
	StartsAt         time.Time    `json:"starts_at"`
	ExpiresAt        time.Time    `json:"expires_at"`
}

// Engine converts the input into a normalized engine permission.
func (in PermissionInput) Engine(id string, version int) engine.Permission {
	if in.Currency == "" {
		in.Currency = "USD"
	}
	return engine.Permission{
		ID: id, Version: version, Name: in.Name,
		MonthlyBudget: in.MonthlyBudget, PerPurchaseCap: in.PerPurchaseCap, ConfirmAbove: in.ConfirmAbove,
		Categories: in.Categories, Merchants: in.Merchants, AskOnNewMerchant: in.AskOnNewMerchant,
		MinReturnDays: in.MinReturnDays, Currency: in.Currency,
		StartsAt: in.StartsAt, ExpiresAt: in.ExpiresAt,
	}.Normalize()
}

// Version is one stored version of a permission.
type Version struct {
	Permission engine.Permission `json:"permission"`
	Hash       []byte            `json:"hash"`
	Signature  []byte            `json:"signature,omitempty"`
	KeyID      string            `json:"key_id,omitempty"`
	SignedAt   *time.Time        `json:"signed_at,omitempty"`
}

// Signed reports whether the version has been signed.
func (v Version) Signed() bool { return len(v.Signature) > 0 }

// PermissionView is a permission with its versions and this month's budget.
type PermissionView struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Status         engine.Status `json:"status"`
	CurrentVersion *int          `json:"current_version"`
	LatestVersion  int           `json:"latest_version"`
	Current        *Version      `json:"current,omitempty"` // the signed version in force
	Latest         Version       `json:"latest"`            // may be an unsigned draft
	Period         string        `json:"period"`
	Budget         engine.Budget `json:"budget"`
	Remaining      engine.Cents  `json:"remaining_cents"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// CreatePermission stores a new draft permission (version 1, unsigned).
func (l *Ledger) CreatePermission(ctx context.Context, in PermissionInput) (*PermissionView, error) {
	id := ids.New("perm")
	p := in.Engine(id, 1)
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	now := l.clock()
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO permissions (id, name, status, latest_version, created_at, updated_at)
			VALUES ($1, $2, 'draft', 1, $3, $3)`, id, p.Name, now); err != nil {
			return err
		}
		if err := insertVersion(ctx, tx, p); err != nil {
			return err
		}
		return addEvent(ctx, tx, now, id, "", "permission.created", map[string]any{"version": 1})
	})
	if err != nil {
		return nil, err
	}
	return l.GetPermission(ctx, id)
}

// NewVersion stores an edited, unsigned version. It takes effect once signed.
func (l *Ledger) NewVersion(ctx context.Context, id string, in PermissionInput) (*PermissionView, error) {
	now := l.clock()
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		var status engine.Status
		var latest int
		err := tx.QueryRow(ctx, `SELECT status, latest_version FROM permissions WHERE id = $1 FOR UPDATE`, id).Scan(&status, &latest)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == engine.StatusRevoked {
			return ErrRevoked
		}
		p := in.Engine(id, latest+1)
		if err := p.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		if err := insertVersion(ctx, tx, p); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE permissions SET latest_version = $2, updated_at = $3 WHERE id = $1`, id, p.Version, now); err != nil {
			return err
		}
		return addEvent(ctx, tx, now, id, "", "permission.version_created", map[string]any{"version": p.Version})
	})
	if err != nil {
		return nil, err
	}
	return l.GetPermission(ctx, id)
}

// Sign signs the given version, which must be the latest and unsigned, and
// makes it the version in force. A draft permission becomes active.
func (l *Ledger) Sign(ctx context.Context, id string, version int) (*PermissionView, error) {
	now := l.clock()
	err := l.inTx(ctx, func(tx pgx.Tx) error {
		var status engine.Status
		var latest int
		err := tx.QueryRow(ctx, `SELECT status, latest_version FROM permissions WHERE id = $1 FOR UPDATE`, id).Scan(&status, &latest)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == engine.StatusRevoked {
			return ErrRevoked
		}
		if version != latest {
			return fmt.Errorf("%w: only the latest version (%d) can be signed", ErrConflict, latest)
		}
		v, err := loadVersion(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if v.Signed() {
			return fmt.Errorf("%w: version %d is already signed", ErrConflict, version)
		}
		sig := l.signer.Sign(v.Permission)
		if _, err := tx.Exec(ctx, `UPDATE permission_versions SET signature = $3, key_id = $4, signed_at = $5
			WHERE permission_id = $1 AND version = $2`, id, version, sig, l.signer.KeyID(), now); err != nil {
			return err
		}
		newStatus := status
		if status == engine.StatusDraft {
			newStatus = engine.StatusActive
		}
		if _, err := tx.Exec(ctx, `UPDATE permissions SET current_version = $2, status = $3, name = $4, updated_at = $5 WHERE id = $1`,
			id, version, newStatus, v.Permission.Name, now); err != nil {
			return err
		}
		h := v.Permission.Hash()
		return addEvent(ctx, tx, now, id, "", "permission.signed", map[string]any{
			"version": version, "hash": fmt.Sprintf("%x", h), "key_id": l.signer.KeyID(),
		})
	})
	if err != nil {
		return nil, err
	}
	return l.GetPermission(ctx, id)
}

// GetPermission returns a permission with its current and latest versions.
func (l *Ledger) GetPermission(ctx context.Context, id string) (*PermissionView, error) {
	return getPermission(ctx, l.pool, id, l.clock())
}

// ListPermissions returns all permissions, newest first.
func (l *Ledger) ListPermissions(ctx context.Context) ([]PermissionView, error) {
	rows, err := l.pool.Query(ctx, `SELECT id FROM permissions ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	idList, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]PermissionView, 0, len(idList))
	now := l.clock()
	for _, id := range idList {
		v, err := getPermission(ctx, l.pool, id, now)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func getPermission(ctx context.Context, q querier, id string, now time.Time) (*PermissionView, error) {
	var v PermissionView
	err := q.QueryRow(ctx, `SELECT id, name, status, current_version, latest_version, created_at, updated_at
		FROM permissions WHERE id = $1`, id).
		Scan(&v.ID, &v.Name, &v.Status, &v.CurrentVersion, &v.LatestVersion, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	latest, err := loadVersion(ctx, q, id, v.LatestVersion)
	if err != nil {
		return nil, err
	}
	v.Latest = *latest
	if v.CurrentVersion != nil {
		cur, err := loadVersion(ctx, q, id, *v.CurrentVersion)
		if err != nil {
			return nil, err
		}
		v.Current = cur
	}
	v.Period = Period(now)
	err = q.QueryRow(ctx, `SELECT committed_cents, held_cents FROM budget_periods WHERE permission_id = $1 AND period = $2`,
		id, v.Period).Scan(&v.Budget.Committed, &v.Budget.Held)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	budget := v.Latest.Permission.MonthlyBudget
	if v.Current != nil {
		budget = v.Current.Permission.MonthlyBudget
	}
	v.Remaining = max(budget-v.Budget.Spent(), 0)
	return &v, nil
}

func insertVersion(ctx context.Context, tx pgx.Tx, p engine.Permission) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	h := p.Hash()
	_, err = tx.Exec(ctx, `INSERT INTO permission_versions (permission_id, version, body, canonical, hash)
		VALUES ($1, $2, $3, $4, $5)`, p.ID, p.Version, body, p.Canonical(), h[:])
	return err
}

func loadVersion(ctx context.Context, q querier, id string, version int) (*Version, error) {
	var v Version
	var body []byte
	var keyID *string
	err := q.QueryRow(ctx, `SELECT body, hash, signature, key_id, signed_at FROM permission_versions
		WHERE permission_id = $1 AND version = $2`, id, version).Scan(&body, &v.Hash, &v.Signature, &keyID, &v.SignedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &v.Permission); err != nil {
		return nil, fmt.Errorf("ledger: decode permission %s v%d: %w", id, version, err)
	}
	if keyID != nil {
		v.KeyID = *keyID
	}
	return &v, nil
}
