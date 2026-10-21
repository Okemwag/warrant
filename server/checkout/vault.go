package checkout

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/server/ids"
)

// Connection is the state of the shopper's saved PayPal account (Mode B).
type Connection struct {
	Status     string    `json:"status"` // none, pending, connected, failed
	ApproveURL string    `json:"approve_url,omitempty"`
	VaultHint  string    `json:"vault_hint,omitempty"` // last 4 characters only
	UpdatedAt  time.Time `json:"updated_at,omitzero"`
}

// Connect starts saving the shopper's PayPal account. The shopper must open
// the returned approve URL once.
func (s *Service) Connect(ctx context.Context) (*Connection, error) {
	st, err := s.pp.CreatePayPalSetupToken(ctx, ids.New("st"),
		"Purchases your shopping agent makes within your Warrant permission",
		s.cfg.PublicURL+"/paypal/vault/return", s.cfg.PublicURL+"/paypal/vault/cancel")
	if err != nil {
		return nil, s.logPayPal("create setup token", err)
	}
	approve := paypal.LinkHref(st.Links, "approve")
	_, err = s.pool.Exec(ctx, `INSERT INTO paypal_vault (id, setup_token_id, vault_id, status, approve_url, updated_at)
		VALUES ('default', $1, NULL, 'pending', $2, $3)
		ON CONFLICT (id) DO UPDATE SET setup_token_id = $1, vault_id = NULL, status = 'pending', approve_url = $2, updated_at = $3`,
		st.ID, approve, s.now())
	if err != nil {
		return nil, err
	}
	return &Connection{Status: "pending", ApproveURL: approve, UpdatedAt: s.now()}, nil
}

// FinishConnect turns an approved setup token into a saved payment token.
func (s *Service) FinishConnect(ctx context.Context) (*Connection, error) {
	var setupID *string
	var status string
	err := s.pool.QueryRow(ctx, `SELECT setup_token_id, status FROM paypal_vault WHERE id = 'default'`).Scan(&setupID, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return &Connection{Status: "none"}, nil
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" || setupID == nil {
		return s.Connection(ctx)
	}
	st, err := s.pp.GetSetupToken(ctx, *setupID)
	if err != nil {
		return nil, s.logPayPal("get setup token", err)
	}
	if st.Status != paypal.SetupTokenApproved {
		return s.Connection(ctx)
	}
	pt, err := s.pp.CreatePaymentToken(ctx, "pt:"+*setupID, *setupID)
	if err != nil {
		_, _ = s.pool.Exec(ctx, `UPDATE paypal_vault SET status = 'failed', updated_at = $1 WHERE id = 'default'`, s.now())
		return nil, s.logPayPal("create payment token", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE paypal_vault SET vault_id = $1, status = 'connected', approve_url = NULL, updated_at = $2 WHERE id = 'default'`,
		pt.ID, s.now()); err != nil {
		return nil, err
	}
	s.cfg.Logger.Info("paypal account connected", "vault_hint", hint(pt.ID))
	return s.Connection(ctx)
}

// Connection reports the saved-account state.
func (s *Service) Connection(ctx context.Context) (*Connection, error) {
	var c Connection
	var vaultID, approve *string
	err := s.pool.QueryRow(ctx, `SELECT status, vault_id, approve_url, updated_at FROM paypal_vault WHERE id = 'default'`).
		Scan(&c.Status, &vaultID, &approve, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &Connection{Status: "none"}, nil
	}
	if err != nil {
		return nil, err
	}
	if vaultID != nil {
		c.VaultHint = hint(*vaultID)
	}
	if approve != nil && c.Status == "pending" {
		c.ApproveURL = *approve
	}
	return &c, nil
}

// Disconnect deletes the saved PayPal account at PayPal and here.
func (s *Service) Disconnect(ctx context.Context) error {
	vaultID, err := s.vaultID(ctx)
	if err == nil {
		if err := s.pp.DeletePaymentToken(ctx, vaultID); err != nil {
			_ = s.logPayPal("delete payment token", err)
		}
	} else if !errors.Is(err, ErrNoVault) {
		return err
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM paypal_vault WHERE id = 'default'`)
	return err
}

func (s *Service) vaultID(ctx context.Context) (string, error) {
	var vaultID *string
	err := s.pool.QueryRow(ctx, `SELECT vault_id FROM paypal_vault WHERE id = 'default' AND status = 'connected'`).Scan(&vaultID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && vaultID == nil) {
		return "", ErrNoVault
	}
	if err != nil {
		return "", err
	}
	return *vaultID, nil
}

func hint(id string) string {
	if len(id) <= 4 {
		return "****"
	}
	return "****" + id[len(id)-4:]
}
