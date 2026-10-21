package checkout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/db/testdb"
	"github.com/okemwag/warrant/server/ledger"
	"github.com/okemwag/warrant/server/signer"
)

var ctx = context.Background()

func intp(i int) *int { return &i }

// fakePayPal is an in-memory PayPal sandbox.
type fakePayPal struct {
	mu            sync.Mutex
	orders        map[string]*paypal.Order
	createCalls   []paypal.CreateOrderRequest
	createdByReq  map[string]string
	voided        []string
	captureStatus string // default COMPLETED
	captureErr    error
	authorizeAdd  int64 // PayPal authorizes this many extra cents
	setupApproved bool
	verifyOK      bool
	refunds       []string
	n             int
}

func newFake() *fakePayPal {
	return &fakePayPal{orders: map[string]*paypal.Order{}, createdByReq: map[string]string{}, captureStatus: paypal.CaptureCompleted, verifyOK: true}
}

func (f *fakePayPal) CreateOrder(_ context.Context, reqID string, req paypal.CreateOrderRequest) (*paypal.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.createdByReq[reqID]; ok { // PayPal-Request-Id idempotency
		return f.orders[id], nil
	}
	f.n++
	f.createCalls = append(f.createCalls, req)
	o := &paypal.Order{ID: fmt.Sprintf("ORDER%d", f.n), Status: paypal.OrderCreated, Intent: req.Intent}
	o.PurchaseUnits = make([]struct {
		ReferenceID string       `json:"reference_id"`
		CustomID    string       `json:"custom_id"`
		InvoiceID   string       `json:"invoice_id"`
		Amount      paypal.Money `json:"amount"`
		Payments    struct {
			Authorizations []paypal.Authorization `json:"authorizations"`
			Captures       []paypal.Capture       `json:"captures"`
		} `json:"payments"`
	}, 1)
	o.PurchaseUnits[0].CustomID = req.PurchaseUnits[0].CustomID
	o.PurchaseUnits[0].Amount = req.PurchaseUnits[0].Amount
	if req.PaymentSource != nil && req.PaymentSource.PayPal != nil && req.PaymentSource.PayPal.VaultID != "" {
		f.authorize(o)
		o.Status = paypal.OrderCompleted
	} else {
		o.Links = []paypal.Link{{Rel: "payer-action", Href: "https://sandbox.paypal.test/checkoutnow?token=" + o.ID}}
	}
	f.orders[o.ID] = o
	f.createdByReq[reqID] = o.ID
	return o, nil
}

func (f *fakePayPal) authorize(o *paypal.Order) {
	cents, _ := paypal.ParseCents(o.PurchaseUnits[0].Amount.Value)
	o.PurchaseUnits[0].Payments.Authorizations = []paypal.Authorization{{ID: "AUTH-" + o.ID, Status: "CREATED", Amount: paypal.USD(cents + f.authorizeAdd)}}
}

func (f *fakePayPal) approve(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.orders[id].Status = paypal.OrderApproved
}

func (f *fakePayPal) GetOrder(_ context.Context, id string) (*paypal.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.orders[id]
	if !ok {
		return nil, &paypal.APIError{Status: 404, Name: "RESOURCE_NOT_FOUND"}
	}
	return o, nil
}

func (f *fakePayPal) AuthorizeOrder(_ context.Context, _, id string) (*paypal.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.orders[id]
	if o.Status != paypal.OrderApproved {
		return nil, &paypal.APIError{Status: 422, Name: "ORDER_NOT_APPROVED", DebugID: "dbg1"}
	}
	f.authorize(o)
	o.Status = paypal.OrderCompleted
	return o, nil
}

func (f *fakePayPal) CaptureAuthorization(_ context.Context, _, authID string) (*paypal.Capture, error) {
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	return &paypal.Capture{ID: "CAP-" + authID, Status: f.captureStatus}, nil
}

func (f *fakePayPal) VoidAuthorization(_ context.Context, authID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.voided = append(f.voided, authID)
	return nil
}

func (f *fakePayPal) GetCapture(_ context.Context, id string) (*paypal.Capture, error) {
	return &paypal.Capture{ID: id, Status: f.captureStatus}, nil
}

func (f *fakePayPal) RefundCapture(_ context.Context, _, captureID, _ string) (*paypal.Refund, error) {
	f.refunds = append(f.refunds, captureID)
	return &paypal.Refund{ID: "REF-" + captureID, Status: "COMPLETED"}, nil
}

func (f *fakePayPal) CreatePayPalSetupToken(context.Context, string, string, string, string) (*paypal.SetupToken, error) {
	return &paypal.SetupToken{ID: "SETUP1", Status: "PAYER_ACTION_REQUIRED", Links: []paypal.Link{{Rel: "approve", Href: "https://sandbox.paypal.test/agreements/approve?token=SETUP1"}}}, nil
}

func (f *fakePayPal) GetSetupToken(_ context.Context, id string) (*paypal.SetupToken, error) {
	status := "PAYER_ACTION_REQUIRED"
	if f.setupApproved {
		status = paypal.SetupTokenApproved
	}
	return &paypal.SetupToken{ID: id, Status: status}, nil
}

func (f *fakePayPal) CreatePaymentToken(context.Context, string, string) (*paypal.PaymentToken, error) {
	return &paypal.PaymentToken{ID: "VAULT-TOKEN-9876"}, nil
}

func (f *fakePayPal) DeletePaymentToken(context.Context, string) error { return nil }

func (f *fakePayPal) VerifyWebhook(context.Context, string, paypal.WebhookHeaders, []byte) (bool, error) {
	return f.verifyOK, nil
}

type fixture struct {
	svc  *Service
	l    *ledger.Ledger
	pp   *fakePayPal
	perm string
}

func setup(t *testing.T, mode string) fixture {
	t.Helper()
	pool := testdb.New(t, "checkout")
	sig, err := signer.FromBase64Seed(signer.NewSeed())
	if err != nil {
		t.Fatal(err)
	}
	l := ledger.New(pool, sig, ledger.Config{})
	pp := newFake()
	svc := New(pool, l, pp, Config{Mode: mode, PublicURL: "http://localhost:8080", WebhookID: "WH1",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	now := time.Now()
	p, err := l.CreatePermission(ctx, ledger.PermissionInput{
		Name: "Everyday errands", MonthlyBudget: 10000, PerPurchaseCap: 4000, ConfirmAbove: 2500,
		Categories: []string{"household"}, Merchants: []string{"demo-store"}, AskOnNewMerchant: true,
		MinReturnDays: 14, Currency: "USD", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Sign(ctx, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	return fixture{svc: svc, l: l, pp: pp, perm: p.ID}
}

func (f fixture) decide(t *testing.T, key string, cents engine.Cents) *ledger.DecisionRecord {
	t.Helper()
	d, err := f.l.Evaluate(ctx, key, engine.Proposal{
		PermissionID: f.perm, Merchant: "demo-store", Category: "household", Amount: cents, Currency: "USD",
		ReturnWindowDays: intp(30), ReturnTermsVerified: true, InstructionSource: engine.SourceUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f fixture) budget(t *testing.T) engine.Budget {
	t.Helper()
	p, err := f.l.GetPermission(ctx, f.perm)
	if err != nil {
		t.Fatal(err)
	}
	return p.Budget
}

func mustCheckout(t *testing.T, f fixture, decisionID string, final *engine.Cents) *Result {
	t.Helper()
	r, err := f.svc.Checkout(ctx, decisionID, final)
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return r
}

func TestModeA_BuyerApprovesThenCaptures(t *testing.T) {
	f := setup(t, ModeApprove)
	d := f.decide(t, "a", 1800)

	r := mustCheckout(t, f, d.ID, nil)
	if r.Status != StatusAwaitingBuyer || r.ApproveURL == "" || r.OrderID == "" {
		t.Fatalf("first call: %+v", r)
	}
	req := f.pp.createCalls[0]
	if req.Intent != paypal.IntentAuthorize || req.PurchaseUnits[0].CustomID != d.ID || req.PurchaseUnits[0].Amount.Value != "18.00" {
		t.Fatalf("order request %+v", req)
	}
	// Calling again before approval just waits.
	if r2 := mustCheckout(t, f, d.ID, nil); r2.Status != StatusAwaitingBuyer || len(f.pp.createCalls) != 1 {
		t.Fatalf("second call: %+v (creates %d)", r2, len(f.pp.createCalls))
	}
	f.pp.approve(r.OrderID)
	r = mustCheckout(t, f, d.ID, nil)
	if r.Status != StatusCompleted || r.CaptureID == "" {
		t.Fatalf("after approval: %+v", r)
	}
	if b := f.budget(t); b.Committed != 1800 || b.Held != 0 {
		t.Fatalf("budget %+v", b)
	}
	got, _ := f.l.GetDecision(ctx, d.ID)
	if got.Status() != "completed" {
		t.Fatalf("decision status %s", got.Status())
	}
}

func TestModeB_VaultPaysWithoutRedirect(t *testing.T) {
	f := setup(t, ModeVault)
	c, err := f.svc.Connect(ctx)
	if err != nil || c.Status != "pending" || c.ApproveURL == "" {
		t.Fatalf("connect: %+v %v", c, err)
	}
	if c, _ := f.svc.FinishConnect(ctx); c.Status != "pending" {
		t.Fatalf("finish before approval: %+v", c)
	}
	f.pp.setupApproved = true
	c, err = f.svc.FinishConnect(ctx)
	if err != nil || c.Status != "connected" || c.VaultHint != "****9876" {
		t.Fatalf("finish: %+v %v", c, err)
	}

	d := f.decide(t, "b", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	if r.Status != StatusCompleted || r.Mode != ModeVault {
		t.Fatalf("vault checkout: %+v", r)
	}
	ps := f.pp.createCalls[0].PaymentSource.PayPal
	if ps.VaultID != "VAULT-TOKEN-9876" || ps.StoredCredential == nil || ps.StoredCredential.PaymentInitiator != "MERCHANT" {
		t.Fatalf("payment source %+v", ps)
	}
	if b := f.budget(t); b.Committed != 1800 {
		t.Fatalf("budget %+v", b)
	}
}

func TestModeB_WithoutConnectionFallsBackToApproval(t *testing.T) {
	f := setup(t, ModeVault)
	d := f.decide(t, "c", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	if r.Status != StatusAwaitingBuyer || r.Mode != ModeApprove {
		t.Fatalf("got %+v", r)
	}
}

func TestPriceDriftBeforePayPal(t *testing.T) {
	f := setup(t, ModeApprove)
	d := f.decide(t, "d", 1800)
	final := engine.Cents(2100)
	r := mustCheckout(t, f, d.ID, &final)
	if r.Status != StatusPriceChanged || r.NewDecision == nil {
		t.Fatalf("got %+v", r)
	}
	nd := r.NewDecision
	if nd.Outcome != engine.Confirm || nd.Reasons[0] != engine.PriceDrift || nd.Explanation != "The price changed from $18 to $21 at checkout." {
		t.Fatalf("new decision %s %v %q", nd.Outcome, nd.Reasons, nd.Explanation)
	}
	if len(f.pp.createCalls) != 0 {
		t.Fatal("PayPal was called despite drift")
	}
	if b := f.budget(t); b.Held != 2100 {
		t.Fatalf("held %d, want only the new 2100", b.Held)
	}
	// The new decision needs approval before checkout.
	if r := mustCheckout(t, f, nd.ID, nil); r.Status != StatusAwaitingApproval {
		t.Fatalf("new decision checkout: %+v", r)
	}
	// The old decision is released.
	if r := mustCheckout(t, f, d.ID, nil); r.Status != StatusReleased {
		t.Fatalf("old decision checkout: %+v", r)
	}
}

func TestPriceDriftPastCapBlocks(t *testing.T) {
	f := setup(t, ModeApprove)
	d := f.decide(t, "e", 1800)
	final := engine.Cents(4500) // over the $40 per-purchase cap
	r := mustCheckout(t, f, d.ID, &final)
	if r.Status != StatusPriceChanged || r.NewDecision.Outcome != engine.Block {
		t.Fatalf("got %+v", r)
	}
	if b := f.budget(t); b.Held != 0 {
		t.Fatalf("held %d", b.Held)
	}
}

func TestAuthorizedAmountMismatchVoids(t *testing.T) {
	f := setup(t, ModeApprove)
	f.pp.authorizeAdd = 300 // PayPal holds $3 more than approved
	d := f.decide(t, "g", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	orderID := r.OrderID
	f.pp.approve(orderID)
	r = mustCheckout(t, f, d.ID, nil)
	if r.Status != StatusPriceChanged || !slices.Contains(f.pp.voided, "AUTH-"+orderID) {
		t.Fatalf("got %+v voided %v", r, f.pp.voided)
	}
	if r.NewDecision.Amount != 2100 || r.NewDecision.Outcome != engine.Confirm {
		t.Fatalf("new decision %+v", r.NewDecision)
	}
	o, _ := f.svc.OrderForDecision(ctx, d.ID)
	if o.Status != "voided" {
		t.Fatalf("order status %s", o.Status)
	}
}

func TestCaptureFailureReleasesAndVoids(t *testing.T) {
	f := setup(t, ModeApprove)
	f.pp.captureErr = &paypal.APIError{Status: 422, Name: "INSTRUMENT_DECLINED", DebugID: "dbg-cap"}
	d := f.decide(t, "h", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	f.pp.approve(r.OrderID)
	r = mustCheckout(t, f, d.ID, nil)
	if r.Status != StatusFailed || len(f.pp.voided) != 1 {
		t.Fatalf("got %+v voided %v", r, f.pp.voided)
	}
	if b := f.budget(t); b.Held != 0 || b.Committed != 0 {
		t.Fatalf("budget %+v", b)
	}
	o, _ := f.svc.OrderForDecision(ctx, d.ID)
	if o.DebugID == nil || *o.DebugID != "dbg-cap" {
		t.Fatalf("debug id %v", o.DebugID)
	}
}

func TestPendingCaptureSettlesLater(t *testing.T) {
	f := setup(t, ModeApprove)
	f.pp.captureStatus = "PENDING"
	d := f.decide(t, "i", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	f.pp.approve(r.OrderID)
	if r = mustCheckout(t, f, d.ID, nil); r.Status != StatusPending {
		t.Fatalf("got %+v", r)
	}
	if b := f.budget(t); b.Held != 1800 || b.Committed != 0 {
		t.Fatalf("pending budget %+v", b)
	}
	f.pp.captureStatus = paypal.CaptureCompleted
	if r = mustCheckout(t, f, d.ID, nil); r.Status != StatusCompleted {
		t.Fatalf("reconcile: %+v", r)
	}
	if b := f.budget(t); b.Committed != 1800 {
		t.Fatalf("budget %+v", b)
	}
}

func TestNotReadyDecisions(t *testing.T) {
	f := setup(t, ModeApprove)
	confirm := f.decide(t, "j", 3000)
	if r := mustCheckout(t, f, confirm.ID, nil); r.Status != StatusAwaitingApproval {
		t.Fatalf("confirm: %+v", r)
	}
	if _, err := f.l.Approve(ctx, confirm.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := mustCheckout(t, f, confirm.ID, nil); r.Status != StatusAwaitingBuyer {
		t.Fatalf("approved confirm: %+v", r)
	}
	blocked := f.decide(t, "k", 9000)
	if r := mustCheckout(t, f, blocked.ID, nil); r.Status != StatusBlocked || r.Message != "This is above your $40 per-purchase limit." {
		t.Fatalf("blocked: %+v", r)
	}
	if _, err := f.svc.Checkout(ctx, "dec_missing", nil); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestCancelReleases(t *testing.T) {
	f := setup(t, ModeApprove)
	d := f.decide(t, "l", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	if r, err := f.svc.Cancel(ctx, r.OrderID); err != nil || r.Status != StatusFailed {
		t.Fatalf("cancel: %+v %v", r, err)
	}
	if b := f.budget(t); b.Held != 0 {
		t.Fatalf("budget %+v", b)
	}
}

func TestRefund(t *testing.T) {
	f := setup(t, ModeApprove)
	d := f.decide(t, "m", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	f.pp.approve(r.OrderID)
	mustCheckout(t, f, d.ID, nil)
	o, err := f.svc.Refund(ctx, r.OrderID)
	if err != nil || o.Status != "refunded" || o.RefundID == nil {
		t.Fatalf("refund: %+v %v", o, err)
	}
	if b := f.budget(t); b.Committed != 0 {
		t.Fatalf("budget %+v", b)
	}
	if _, err := f.svc.Refund(ctx, r.OrderID); err != nil {
		t.Fatalf("second refund: %v", err)
	}
	if len(f.pp.refunds) != 1 {
		t.Fatalf("refund calls %d", len(f.pp.refunds))
	}
}

func webhookBody(id, typ, resource string) []byte {
	return fmt.Appendf(nil, `{"id":%q,"event_type":%q,"resource":%s}`, id, typ, resource)
}

func TestWebhooks(t *testing.T) {
	f := setup(t, ModeApprove)
	f.pp.captureStatus = "PENDING"
	d := f.decide(t, "n", 1800)
	r := mustCheckout(t, f, d.ID, nil)
	f.pp.approve(r.OrderID)

	// Buyer approved: the webhook drives authorize + capture.
	if err := f.svc.HandleWebhook(ctx, paypal.WebhookHeaders{}, webhookBody("WH-1", "CHECKOUT.ORDER.APPROVED", fmt.Sprintf(`{"id":%q}`, r.OrderID))); err != nil {
		t.Fatal(err)
	}
	o, _ := f.svc.OrderForDecision(ctx, d.ID)
	if o.Status != "captured" || o.CaptureID == nil {
		t.Fatalf("after approved webhook: %s", o.Status)
	}
	capture := fmt.Sprintf(`{"id":%q,"status":"COMPLETED","custom_id":%q,"supplementary_data":{"related_ids":{"order_id":%q}}}`, *o.CaptureID, d.ID, o.OrderID)
	if err := f.svc.HandleWebhook(ctx, paypal.WebhookHeaders{}, webhookBody("WH-2", "PAYMENT.CAPTURE.COMPLETED", capture)); err != nil {
		t.Fatal(err)
	}
	if b := f.budget(t); b.Committed != 1800 || b.Held != 0 {
		t.Fatalf("budget %+v", b)
	}
	// Duplicate delivery is ignored.
	if err := f.svc.HandleWebhook(ctx, paypal.WebhookHeaders{}, webhookBody("WH-2", "PAYMENT.CAPTURE.COMPLETED", capture)); err != nil {
		t.Fatal(err)
	}
	if b := f.budget(t); b.Committed != 1800 {
		t.Fatalf("duplicate changed budget %+v", b)
	}
	// Refund made in the PayPal dashboard: the refund resource links up to the capture.
	refund := fmt.Sprintf(`{"id":"REF9","status":"COMPLETED","links":[{"rel":"up","href":"https://api-m.sandbox.paypal.com/v2/payments/captures/%s"}]}`, *o.CaptureID)
	if err := f.svc.HandleWebhook(ctx, paypal.WebhookHeaders{}, webhookBody("WH-3", "PAYMENT.CAPTURE.REFUNDED", refund)); err != nil {
		t.Fatal(err)
	}
	if b := f.budget(t); b.Committed != 0 {
		t.Fatalf("after refund webhook %+v", b)
	}
}

func TestWebhooksRejectUnverified(t *testing.T) {
	f := setup(t, ModeApprove)
	f.pp.verifyOK = false
	err := f.svc.HandleWebhook(ctx, paypal.WebhookHeaders{}, webhookBody("WH-X", "PAYMENT.CAPTURE.COMPLETED", `{"id":"x"}`))
	if !errors.Is(err, ErrWebhookUnverified) {
		t.Fatalf("err = %v", err)
	}
	f.svc.cfg.WebhookID = ""
	if err := f.svc.HandleWebhook(ctx, paypal.WebhookHeaders{}, []byte(`{}`)); !errors.Is(err, ErrWebhookNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}
