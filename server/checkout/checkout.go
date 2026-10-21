// Package checkout turns a permitted decision into a PayPal sandbox payment.
//
// Every order uses intent AUTHORIZE: PayPal holds the funds, Warrant checks
// the authorized amount against the decision, and only then captures. A
// mismatch voids the authorization and sends the purchase back through the
// engine with PRICE_DRIFT. Spend is committed only when PayPal reports the
// capture COMPLETED, either in its API response or in a verified webhook.
package checkout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/ledger"
)

// PayPal is the subset of the PayPal client checkout needs.
type PayPal interface {
	CreateOrder(ctx context.Context, requestID string, req paypal.CreateOrderRequest) (*paypal.Order, error)
	GetOrder(ctx context.Context, id string) (*paypal.Order, error)
	AuthorizeOrder(ctx context.Context, requestID, id string) (*paypal.Order, error)
	CaptureAuthorization(ctx context.Context, requestID, authID string) (*paypal.Capture, error)
	VoidAuthorization(ctx context.Context, authID string) error
	GetCapture(ctx context.Context, captureID string) (*paypal.Capture, error)
	RefundCapture(ctx context.Context, requestID, captureID, note string) (*paypal.Refund, error)
	CreatePayPalSetupToken(ctx context.Context, requestID, description, returnURL, cancelURL string) (*paypal.SetupToken, error)
	GetSetupToken(ctx context.Context, id string) (*paypal.SetupToken, error)
	CreatePaymentToken(ctx context.Context, requestID, setupTokenID string) (*paypal.PaymentToken, error)
	DeletePaymentToken(ctx context.Context, id string) error
	VerifyWebhook(ctx context.Context, webhookID string, h paypal.WebhookHeaders, body []byte) (bool, error)
}

// Modes.
const (
	ModeApprove = "approve" // Mode A: the shopper approves each order on PayPal
	ModeVault   = "vault"   // Mode B: saved PayPal account, no redirect
)

// Result statuses returned to the agent.
const (
	StatusCompleted        = "completed"
	StatusAwaitingBuyer    = "awaiting_buyer_approval"
	StatusAwaitingApproval = "awaiting_approval" // CONFIRM not yet answered in Warrant
	StatusPriceChanged     = "price_changed"
	StatusBlocked          = "blocked"
	StatusReleased         = "released"
	StatusFailed           = "failed"
	StatusPending          = "capture_pending"
	StatusRefunded         = "refunded"
)

// Config configures the service.
type Config struct {
	Mode      string
	Tolerance engine.Cents
	PublicURL string // base URL PayPal redirects the buyer back to
	BrandName string
	WebhookID string
	Logger    *slog.Logger
	Now       func() time.Time
}

// Service runs checkouts.
type Service struct {
	pool *pgxpool.Pool
	l    *ledger.Ledger
	pp   PayPal
	cfg  Config
}

// New returns a Service.
func New(pool *pgxpool.Pool, l *ledger.Ledger, pp PayPal, cfg Config) *Service {
	if cfg.Mode != ModeVault {
		cfg.Mode = ModeApprove
	}
	if cfg.BrandName == "" {
		cfg.BrandName = "Warrant Demo Store"
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{pool: pool, l: l, pp: pp, cfg: cfg}
}

// Mode reports the configured payment mode.
func (s *Service) Mode() string { return s.cfg.Mode }

// Result is what the agent gets back from checkout.
type Result struct {
	DecisionID  string                 `json:"decision_id"`
	Status      string                 `json:"status"`
	Message     string                 `json:"message"`
	Mode        string                 `json:"mode,omitempty"`
	OrderID     string                 `json:"order_id,omitempty"`
	CaptureID   string                 `json:"capture_id,omitempty"`
	ApproveURL  string                 `json:"approve_url,omitempty"`
	Amount      engine.Cents           `json:"amount_cents,omitempty"`
	NewDecision *ledger.DecisionRecord `json:"new_decision,omitempty"`
}

// Order is a stored PayPal order.
type Order struct {
	OrderID         string       `json:"order_id"`
	DecisionID      string       `json:"decision_id"`
	ReservationID   string       `json:"reservation_id"`
	PermissionID    string       `json:"permission_id"`
	Mode            string       `json:"mode"`
	InvoiceID       string       `json:"invoice_id"`
	Quoted          engine.Cents `json:"quoted_cents"`
	Final           engine.Cents `json:"final_cents"`
	Authorized      *int64       `json:"authorized_cents,omitempty"`
	AuthorizationID *string      `json:"authorization_id,omitempty"`
	CaptureID       *string      `json:"capture_id,omitempty"`
	Status          string       `json:"status"`
	ApproveURL      *string      `json:"approve_url,omitempty"`
	Failure         *string      `json:"failure,omitempty"`
	DebugID         *string      `json:"debug_id,omitempty"`
	RefundID        *string      `json:"refund_id,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// ErrNoVault means Mode B was asked for but no PayPal account is connected.
var ErrNoVault = errors.New("checkout: no PayPal account connected")

// Checkout pays for a decision. Calling it again for the same decision
// continues where it left off, so an agent can simply call it until the
// status is final.
func (s *Service) Checkout(ctx context.Context, decisionID string, final *engine.Cents) (*Result, error) {
	if o, err := s.orderByDecision(ctx, decisionID); err == nil {
		return s.continueOrder(ctx, o)
	} else if !errors.Is(err, ledger.ErrNotFound) {
		return nil, err
	}

	d, err := s.l.GetDecision(ctx, decisionID)
	if err != nil {
		return nil, err
	}
	switch d.Status() {
	case "blocked":
		return &Result{DecisionID: d.ID, Status: StatusBlocked, Message: d.Explanation}, nil
	case "awaiting_approval":
		return &Result{DecisionID: d.ID, Status: StatusAwaitingApproval, Message: "Waiting for the person to approve this purchase in Warrant. " + d.Explanation}, nil
	case "released":
		return &Result{DecisionID: d.ID, Status: StatusReleased, Message: "This purchase no longer holds budget (declined, expired or the permission was paused). Ask again with evaluate."}, nil
	case "completed":
		return &Result{DecisionID: d.ID, Status: StatusCompleted, Message: "Already paid."}, nil
	}

	amount := d.Amount
	if final != nil {
		if *final <= 0 || *final > engine.MaxAmount {
			return nil, fmt.Errorf("%w: final amount must be positive", ledger.ErrInvalid)
		}
		amount = *final
	}
	if _, drift := engine.CheckPrice(d.Amount, amount, s.cfg.Tolerance); drift {
		return s.priceChanged(ctx, d, amount)
	}
	if err := s.l.ExtendHold(ctx, d.Reservation.ID); err != nil {
		return nil, err
	}

	mode := s.cfg.Mode
	var vaultID string
	if mode == ModeVault {
		vaultID, err = s.vaultID(ctx)
		if errors.Is(err, ErrNoVault) {
			s.cfg.Logger.Warn("vault mode without a connected PayPal account; using buyer approval", "decision", d.ID)
			mode = ModeApprove
		} else if err != nil {
			return nil, err
		}
	}

	req := paypal.CreateOrderRequest{
		Intent: paypal.IntentAuthorize,
		PurchaseUnits: []paypal.PurchaseUnit{{
			ReferenceID: "default", CustomID: d.ID, InvoiceID: "wr_" + d.ID,
			Description: truncate(fmt.Sprintf("%s via Warrant (%s)", d.Proposal.Category, d.Proposal.Merchant), 127),
			Amount:      paypal.USD(int64(amount)),
		}},
	}
	if mode == ModeVault {
		req.PaymentSource = &paypal.PaymentSource{PayPal: &paypal.PayPalSource{VaultID: vaultID, StoredCredential: paypal.MerchantInitiated()}}
	} else {
		req.PaymentSource = &paypal.PaymentSource{PayPal: &paypal.PayPalSource{ExperienceContext: &paypal.ExperienceContext{
			BrandName: s.cfg.BrandName, ShippingPreference: "NO_SHIPPING", UserAction: "CONTINUE",
			ReturnURL: s.cfg.PublicURL + "/paypal/return", CancelURL: s.cfg.PublicURL + "/paypal/cancel",
		}}}
	}

	ppOrder, err := s.pp.CreateOrder(ctx, d.ID+":create", req)
	if err != nil {
		_ = s.l.Release(ctx, d.Reservation.ID, ledger.ReleasePayFailed)
		return &Result{DecisionID: d.ID, Status: StatusFailed, Message: "PayPal couldn't create the order; the budget hold was released.", Mode: mode}, s.logPayPal("create order", err)
	}
	o := &Order{
		OrderID: ppOrder.ID, DecisionID: d.ID, ReservationID: d.Reservation.ID, PermissionID: d.PermissionID,
		Mode: mode, InvoiceID: "wr_" + d.ID, Quoted: d.Amount, Final: amount, Status: "awaiting_buyer",
	}
	if u := ppOrder.ApproveURL(); u != "" {
		o.ApproveURL = &u
	}
	if err := s.insertOrder(ctx, o); err != nil {
		return nil, err
	}
	if mode == ModeVault {
		if ppOrder.Status == paypal.OrderPayerActionRequired {
			return s.result(o, StatusAwaitingBuyer, "PayPal needs the buyer to approve this one. Open approve_url."), nil
		}
		return s.authorizeAndCapture(ctx, o, ppOrder)
	}
	return s.result(o, StatusAwaitingBuyer, "Open approve_url to approve the payment on PayPal, then call checkout again (or wait for the webhook)."), nil
}

// Complete continues an order after the buyer approved it on PayPal.
func (s *Service) Complete(ctx context.Context, orderID string) (*Result, error) {
	o, err := s.orderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return s.continueOrder(ctx, o)
}

// Cancel records that the buyer cancelled on PayPal and releases the hold.
func (s *Service) Cancel(ctx context.Context, orderID string) (*Result, error) {
	o, err := s.orderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o.Status != "awaiting_buyer" {
		return s.continueOrder(ctx, o)
	}
	if err := s.l.Release(ctx, o.ReservationID, ledger.ReleaseDeclined); err != nil && !errors.Is(err, ledger.ErrNotHeld) {
		return nil, err
	}
	if err := s.setStatus(ctx, o, "failed", "buyer cancelled on PayPal", ""); err != nil {
		return nil, err
	}
	return s.result(o, StatusFailed, "The buyer cancelled on PayPal. The budget hold was released."), nil
}

func (s *Service) continueOrder(ctx context.Context, o *Order) (*Result, error) {
	switch o.Status {
	case "completed":
		return s.result(o, StatusCompleted, "Paid."), nil
	case "refunded":
		return s.result(o, StatusRefunded, "Paid, then refunded."), nil
	case "failed", "voided":
		msg := "This checkout did not complete."
		if o.Failure != nil {
			msg += " " + *o.Failure
		}
		return s.result(o, StatusFailed, msg), nil
	case "captured":
		return s.reconcileCapture(ctx, o)
	case "authorized":
		ppOrder, err := s.pp.GetOrder(ctx, o.OrderID)
		if err != nil {
			return nil, s.logPayPal("get order", err)
		}
		return s.authorizeAndCapture(ctx, o, ppOrder)
	}
	// awaiting_buyer
	ppOrder, err := s.pp.GetOrder(ctx, o.OrderID)
	if err != nil {
		return nil, s.logPayPal("get order", err)
	}
	switch ppOrder.Status {
	case paypal.OrderApproved, paypal.OrderCompleted:
		return s.authorizeAndCapture(ctx, o, ppOrder)
	case paypal.OrderVoided:
		_ = s.l.Release(ctx, o.ReservationID, ledger.ReleasePayFailed)
		_ = s.setStatus(ctx, o, "voided", "PayPal voided the order", "")
		return s.result(o, StatusFailed, "PayPal voided the order. The budget hold was released."), nil
	}
	// Still waiting on the buyer; make sure the hold is still ours.
	d, err := s.l.GetDecision(ctx, o.DecisionID)
	if err != nil {
		return nil, err
	}
	if d.Reservation == nil || d.Reservation.State != ledger.StateHeld {
		_ = s.setStatus(ctx, o, "failed", "the budget hold ended before the buyer approved", "")
		return s.result(o, StatusReleased, "The budget hold ended before PayPal approval. Ask again with evaluate."), nil
	}
	return s.result(o, StatusAwaitingBuyer, "Waiting for the buyer to approve on PayPal."), nil
}

// authorizeAndCapture finishes an approved (or vault) order.
func (s *Service) authorizeAndCapture(ctx context.Context, o *Order, ppOrder *paypal.Order) (*Result, error) {
	auth := ppOrder.FirstAuthorization()
	if auth == nil {
		var err error
		ppOrder, err = s.pp.AuthorizeOrder(ctx, o.DecisionID+":authorize", o.OrderID)
		if err != nil {
			return s.fail(ctx, o, nil, "PayPal didn't authorize the payment.", err)
		}
		if auth = ppOrder.FirstAuthorization(); auth == nil {
			return s.fail(ctx, o, nil, "PayPal returned no authorization.", fmt.Errorf("order %s status %s has no authorization", o.OrderID, ppOrder.Status))
		}
	}
	authorized, err := paypal.ParseCents(auth.Amount.Value)
	if err != nil || auth.Amount.CurrencyCode != "USD" {
		return s.fail(ctx, o, &auth.ID, "PayPal authorized an amount Warrant couldn't read.", fmt.Errorf("authorized %+v: %v", auth.Amount, err))
	}
	if _, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET status = 'authorized', authorization_id = $2, authorized_cents = $3, updated_at = $4
		WHERE order_id = $1`, o.OrderID, auth.ID, authorized, s.now()); err != nil {
		return nil, err
	}

	// Compare what PayPal is holding with what the person approved.
	if _, drift := engine.CheckPrice(o.Quoted, engine.Cents(authorized), s.cfg.Tolerance); drift {
		if err := s.pp.VoidAuthorization(ctx, auth.ID); err != nil {
			_ = s.logPayPal("void authorization", err)
		}
		_ = s.setStatus(ctx, o, "voided", fmt.Sprintf("authorized %s but %s was approved", engine.Cents(authorized), o.Quoted), "")
		d, err := s.l.GetDecision(ctx, o.DecisionID)
		if err != nil {
			return nil, err
		}
		return s.priceChanged(ctx, d, engine.Cents(authorized))
	}

	// The hold must still be ours before money moves.
	if err := s.l.ExtendHold(ctx, o.ReservationID); err != nil {
		if verr := s.pp.VoidAuthorization(ctx, auth.ID); verr != nil {
			_ = s.logPayPal("void authorization", verr)
		}
		_ = s.setStatus(ctx, o, "voided", "the budget hold ended before capture", "")
		return s.result(o, StatusReleased, "The budget hold ended before payment, so the PayPal authorization was voided. Ask again with evaluate."), nil
	}

	cp, err := s.pp.CaptureAuthorization(ctx, o.DecisionID+":capture", auth.ID)
	if err != nil {
		return s.fail(ctx, o, &auth.ID, "PayPal didn't capture the payment.", err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET status = 'captured', capture_id = $2, updated_at = $3 WHERE order_id = $1`,
		o.OrderID, cp.ID, s.now()); err != nil {
		return nil, err
	}
	o.CaptureID = &cp.ID
	o.Status = "captured"
	return s.applyCaptureStatus(ctx, o, cp.Status)
}

// reconcileCapture asks PayPal for the capture's current status.
func (s *Service) reconcileCapture(ctx context.Context, o *Order) (*Result, error) {
	if o.CaptureID == nil {
		return s.result(o, StatusPending, "Waiting for PayPal to confirm the capture."), nil
	}
	cp, err := s.pp.GetCapture(ctx, *o.CaptureID)
	if err != nil {
		return nil, s.logPayPal("get capture", err)
	}
	return s.applyCaptureStatus(ctx, o, cp.Status)
}

func (s *Service) applyCaptureStatus(ctx context.Context, o *Order, status string) (*Result, error) {
	switch status {
	case paypal.CaptureCompleted:
		if err := s.markCompleted(ctx, o); err != nil {
			return nil, err
		}
		return s.result(o, StatusCompleted, fmt.Sprintf("Paid %s through PayPal.", o.Final)), nil
	case "DECLINED", "FAILED":
		_ = s.l.Release(ctx, o.ReservationID, ledger.ReleasePayFailed)
		_ = s.setStatus(ctx, o, "failed", "PayPal declined the capture", "")
		return s.result(o, StatusFailed, "PayPal declined the payment. The budget hold was released."), nil
	}
	// PENDING: keep the hold; a webhook or a later checkout call settles it.
	return s.result(o, StatusPending, "PayPal accepted the payment but hasn't completed it yet."), nil
}

func (s *Service) markCompleted(ctx context.Context, o *Order) error {
	if err := s.l.Commit(ctx, o.ReservationID); err != nil {
		return err
	}
	o.Status = "completed"
	_, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET status = 'completed', updated_at = $2 WHERE order_id = $1 AND status <> 'refunded'`, o.OrderID, s.now())
	return err
}

func (s *Service) priceChanged(ctx context.Context, d *ledger.DecisionRecord, amount engine.Cents) (*Result, error) {
	nd, err := s.l.Reevaluate(ctx, d.ID, amount)
	if err != nil {
		return nil, err
	}
	return &Result{
		DecisionID: d.ID, Status: StatusPriceChanged, Amount: amount, NewDecision: nd,
		Message: nd.Explanation + " Use new_decision.decision_id for the next checkout.",
	}, nil
}

// fail voids any authorization, releases the hold and records the failure.
func (s *Service) fail(ctx context.Context, o *Order, authID *string, msg string, cause error) (*Result, error) {
	debugID := ""
	var apiErr *paypal.APIError
	if errors.As(cause, &apiErr) {
		debugID = apiErr.DebugID
	}
	_ = s.logPayPal("checkout", cause)
	if authID != nil {
		if err := s.pp.VoidAuthorization(ctx, *authID); err != nil {
			_ = s.logPayPal("void authorization", err)
		}
	}
	if err := s.l.Release(ctx, o.ReservationID, ledger.ReleasePayFailed); err != nil && !errors.Is(err, ledger.ErrNotHeld) {
		return nil, err
	}
	if err := s.setStatus(ctx, o, "failed", msg, debugID); err != nil {
		return nil, err
	}
	return s.result(o, StatusFailed, msg+" The budget hold was released."), nil
}

// Refund refunds a completed order in full and returns the spend to the budget.
func (s *Service) Refund(ctx context.Context, orderID string) (*Order, error) {
	o, err := s.orderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o.Status == "refunded" {
		return o, nil
	}
	if o.Status != "completed" || o.CaptureID == nil {
		return nil, fmt.Errorf("%w: only completed orders can be refunded", ledger.ErrConflict)
	}
	rf, err := s.pp.RefundCapture(ctx, "refund:"+o.OrderID, *o.CaptureID, "Refunded through Warrant")
	if err != nil {
		return nil, s.logPayPal("refund", err)
	}
	if err := s.l.Refund(ctx, o.ReservationID); err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET status = 'refunded', refund_id = $2, updated_at = $3 WHERE order_id = $1`,
		o.OrderID, rf.ID, s.now()); err != nil {
		return nil, err
	}
	return s.orderByID(ctx, orderID)
}

// ListOrders returns a permission's orders, newest first.
func (s *Service) ListOrders(ctx context.Context, permissionID string) ([]Order, error) {
	rows, err := s.pool.Query(ctx, orderSelect+` WHERE permission_id = $1 ORDER BY created_at DESC LIMIT 200`, permissionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Order, error) {
		o, err := scanOrder(r)
		if err != nil {
			return Order{}, err
		}
		return *o, nil
	})
}

// GetOrder returns one stored order.
func (s *Service) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	return s.orderByID(ctx, orderID)
}

// OrderForDecision returns the order for a decision, if any.
func (s *Service) OrderForDecision(ctx context.Context, decisionID string) (*Order, error) {
	return s.orderByDecision(ctx, decisionID)
}

func (s *Service) result(o *Order, status, msg string) *Result {
	r := &Result{DecisionID: o.DecisionID, Status: status, Message: msg, Mode: o.Mode, OrderID: o.OrderID, Amount: o.Final}
	if o.CaptureID != nil {
		r.CaptureID = *o.CaptureID
	}
	if status == StatusAwaitingBuyer && o.ApproveURL != nil {
		r.ApproveURL = *o.ApproveURL
	}
	return r
}

func (s *Service) logPayPal(op string, err error) error {
	var apiErr *paypal.APIError
	if errors.As(err, &apiErr) {
		s.cfg.Logger.Error("paypal", "op", op, "status", apiErr.Status, "name", apiErr.Name, "debug_id", apiErr.DebugID)
	} else {
		s.cfg.Logger.Error("paypal", "op", op, "err", err)
	}
	return fmt.Errorf("paypal %s: %w", op, err)
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

const orderSelect = `SELECT order_id, decision_id, reservation_id, permission_id, mode, invoice_id, quoted_cents, final_cents,
	authorized_cents, authorization_id, capture_id, status, approve_url, failure, debug_id, refund_id, created_at, updated_at FROM paypal_orders`

func scanOrder(row pgx.Row) (*Order, error) {
	var o Order
	err := row.Scan(&o.OrderID, &o.DecisionID, &o.ReservationID, &o.PermissionID, &o.Mode, &o.InvoiceID, &o.Quoted, &o.Final,
		&o.Authorized, &o.AuthorizationID, &o.CaptureID, &o.Status, &o.ApproveURL, &o.Failure, &o.DebugID, &o.RefundID, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ledger.ErrNotFound
	}
	return &o, err
}

func (s *Service) orderByID(ctx context.Context, id string) (*Order, error) {
	return scanOrder(s.pool.QueryRow(ctx, orderSelect+` WHERE order_id = $1`, id))
}

func (s *Service) orderByDecision(ctx context.Context, decisionID string) (*Order, error) {
	return scanOrder(s.pool.QueryRow(ctx, orderSelect+` WHERE decision_id = $1`, decisionID))
}

func (s *Service) insertOrder(ctx context.Context, o *Order) error {
	now := s.now()
	o.CreatedAt, o.UpdatedAt = now, now
	_, err := s.pool.Exec(ctx, `INSERT INTO paypal_orders (order_id, decision_id, reservation_id, permission_id, mode, invoice_id,
		quoted_cents, final_cents, status, approve_url, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11) ON CONFLICT (order_id) DO NOTHING`,
		o.OrderID, o.DecisionID, o.ReservationID, o.PermissionID, o.Mode, o.InvoiceID, o.Quoted, o.Final, o.Status, o.ApproveURL, now)
	return err
}

func (s *Service) setStatus(ctx context.Context, o *Order, status, failure, debugID string) error {
	o.Status = status
	var f, d *string
	if failure != "" {
		f = &failure
		o.Failure = f
	}
	if debugID != "" {
		d = &debugID
	}
	_, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET status = $2, failure = COALESCE($3, failure), debug_id = COALESCE($4, debug_id), updated_at = $5
		WHERE order_id = $1`, o.OrderID, status, f, d, s.now())
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
