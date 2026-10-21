package ledger

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/db/testdb"
	"github.com/okemwag/warrant/server/signer"
)

var ctx = context.Background()

type testClock struct{ ns atomic.Int64 }

func (c *testClock) Now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *testClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

type fixture struct {
	l     *Ledger
	clock *testClock
}

func setup(t *testing.T, cfg Config) fixture {
	t.Helper()
	pool := testdb.New(t, "ledger")
	s, err := signer.FromBase64Seed("d2FycmFudC1sZWRnZXItdGVzdC1zZWVkLTMyYnl0ZXM=")
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{}
	clk.ns.Store(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC).UnixNano())
	cfg.Now = clk.Now
	return fixture{l: New(pool, s, cfg), clock: clk}
}

func everyday(f fixture) PermissionInput {
	now := f.clock.Now()
	return PermissionInput{
		Name: "Everyday errands", MonthlyBudget: 10000, PerPurchaseCap: 4000, ConfirmAbove: 2500,
		Categories: []string{"household", "groceries"}, Merchants: []string{"demo-store"}, AskOnNewMerchant: true,
		MinReturnDays: 14, Currency: "USD", StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(60 * 24 * time.Hour),
	}
}

func active(t *testing.T, f fixture, in PermissionInput) string {
	t.Helper()
	p, err := f.l.CreatePermission(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.l.Sign(ctx, p.ID, 1); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func days(n int) *int { return &n }

func proposal(permID string, cents engine.Cents) engine.Proposal {
	return engine.Proposal{
		PermissionID: permID, Merchant: "demo-store", Category: "household", Amount: cents, Currency: "USD",
		ReturnWindowDays: days(30), ReturnTermsVerified: true, InstructionSource: engine.SourceUser,
	}
}

func budget(t *testing.T, f fixture, id string) engine.Budget {
	t.Helper()
	p, err := f.l.GetPermission(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Budget
}

func TestPermission_DraftThenSignActivates(t *testing.T) {
	f := setup(t, Config{})
	p, err := f.l.CreatePermission(ctx, everyday(f))
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusDraft || p.CurrentVersion != nil {
		t.Fatalf("new permission: status %s current %v", p.Status, p.CurrentVersion)
	}
	d, err := f.l.Evaluate(ctx, "k1", proposal(p.ID, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != engine.Block || d.Explanation != "This permission hasn't been signed yet." {
		t.Fatalf("draft evaluate: %s %q", d.Outcome, d.Explanation)
	}
	p, err = f.l.Sign(ctx, p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusActive || p.Current == nil || !p.Current.Signed() || p.Current.KeyID == "" {
		t.Fatalf("after sign: %+v", p)
	}
	if _, err := f.l.Sign(ctx, p.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("double sign err = %v", err)
	}
}

func TestPermission_RejectsInvalid(t *testing.T) {
	f := setup(t, Config{})
	in := everyday(f)
	in.PerPurchaseCap = in.MonthlyBudget + 1
	if _, err := f.l.CreatePermission(ctx, in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestEvaluate_AllowHoldsBudget(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	d, err := f.l.Evaluate(ctx, "k1", proposal(id, 1800))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != engine.Allow || d.Reservation == nil || d.Reservation.State != StateHeld || d.Status() != "ready" {
		t.Fatalf("got %+v", d)
	}
	if d.Explanation != "Within your permission: $18 of your $100 monthly budget, 30-day returns." {
		t.Errorf("explanation %q", d.Explanation)
	}
	if b := budget(t, f, id); b.Held != 1800 || b.Committed != 0 {
		t.Fatalf("budget %+v", b)
	}
	if d.Remaining != 8200 {
		t.Errorf("remaining %d", d.Remaining)
	}
}

func TestEvaluate_BlockHoldsNothing(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	p := proposal(id, 1800)
	p.InstructionSource = engine.SourceWeb
	d, err := f.l.Evaluate(ctx, "k1", p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != engine.Block || d.Reservation != nil || !slices.Contains(d.Reasons, engine.UntrustedInstruction) {
		t.Fatalf("got %+v", d)
	}
	if b := budget(t, f, id); b.Spent() != 0 {
		t.Fatalf("budget %+v", b)
	}
}

func TestEvaluate_IdempotentReplay(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	first, err := f.l.Evaluate(ctx, "same", proposal(id, 1800))
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.l.Evaluate(ctx, "same", proposal(id, 1800))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID || !again.Replayed {
		t.Fatalf("replay returned %s (replayed=%v), want %s", again.ID, again.Replayed, first.ID)
	}
	if b := budget(t, f, id); b.Held != 1800 {
		t.Fatalf("held %d after replay, want 1800", b.Held)
	}
}

func TestEvaluate_IdempotentConcurrentSameKey(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	var wg sync.WaitGroup
	got := make([]string, 20)
	for i := range got {
		wg.Go(func() {
			d, err := f.l.Evaluate(ctx, "same", proposal(id, 1800))
			if err != nil {
				t.Error(err)
				return
			}
			got[i] = d.ID
		})
	}
	wg.Wait()
	for _, g := range got {
		if g != got[0] {
			t.Fatalf("different decision ids for the same key: %v", got)
		}
	}
	if b := budget(t, f, id); b.Held != 1800 {
		t.Fatalf("held %d, want 1800", b.Held)
	}
}

// Gate 3: 50 simultaneous purchases can never push committed + held past the
// monthly budget.
func TestEvaluate_ConcurrentNeverExceedsBudget(t *testing.T) {
	f := setup(t, Config{LockTimeout: 10 * time.Second})
	in := everyday(f)
	in.ConfirmAbove = in.PerPurchaseCap // no approvals needed, so all fit-or-block
	id := active(t, f, in)

	var allowed, blocked atomic.Int64
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			d, err := f.l.Evaluate(ctx, fmt.Sprintf("k%d", i), proposal(id, 700))
			if err != nil {
				t.Error(err)
				return
			}
			switch d.Outcome {
			case engine.Allow:
				allowed.Add(1)
			case engine.Block:
				if !slices.Contains(d.Reasons, engine.OverMonthlyBudget) {
					t.Errorf("unexpected block reasons %v", d.Reasons)
				}
				blocked.Add(1)
			default:
				t.Errorf("unexpected outcome %s", d.Outcome)
			}
		})
	}
	wg.Wait()
	b := budget(t, f, id)
	if b.Spent() > 10000 {
		t.Fatalf("OVERSPENT: held %d committed %d > budget 10000", b.Held, b.Committed)
	}
	if allowed.Load() != 14 || b.Held != 9800 {
		t.Fatalf("allowed %d held %d, want 14 and 9800", allowed.Load(), b.Held)
	}
	if blocked.Load() != 36 {
		t.Fatalf("blocked %d, want 36", blocked.Load())
	}
}

// Same invariant with random amounts and commits/releases racing evaluations.
func TestEvaluate_ConcurrentMixedOperations(t *testing.T) {
	f := setup(t, Config{LockTimeout: 10 * time.Second})
	in := everyday(f)
	in.ConfirmAbove = in.PerPurchaseCap
	id := active(t, f, in)

	var wg sync.WaitGroup
	for i := range 60 {
		wg.Go(func() {
			amt := engine.Cents(100 + (i*397)%3900)
			d, err := f.l.Evaluate(ctx, fmt.Sprintf("m%d", i), proposal(id, amt))
			if err != nil {
				t.Error(err)
				return
			}
			if d.Reservation == nil {
				return
			}
			switch i % 3 {
			case 0:
				err = f.l.Commit(ctx, d.Reservation.ID)
			case 1:
				err = f.l.Release(ctx, d.Reservation.ID, ReleasePayFailed)
			}
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b := budget(t, f, id)
	if b.Spent() > 10000 || b.Held < 0 || b.Committed < 0 {
		t.Fatalf("budget invariant broken: %+v", b)
	}
	// Ledger totals must match the reservations table exactly.
	var held, committed engine.Cents
	err := f.l.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_cents) FILTER (WHERE state='held'),0),
		COALESCE(SUM(amount_cents) FILTER (WHERE state='committed'),0) FROM reservations WHERE permission_id=$1`, id).Scan(&held, &committed)
	if err != nil {
		t.Fatal(err)
	}
	if held != b.Held || committed != b.Committed {
		t.Fatalf("budget row %+v disagrees with reservations held=%d committed=%d", b, held, committed)
	}
}

func TestEvaluate_LockTimeoutIsBudgetConflict(t *testing.T) {
	f := setup(t, Config{LockTimeout: 100 * time.Millisecond})
	id := active(t, f, everyday(f))
	// Make sure the budget row exists, then hold its lock in another transaction.
	if _, err := f.l.Evaluate(ctx, "warm", proposal(id, 100)); err != nil {
		t.Fatal(err)
	}
	tx, err := f.l.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM budget_periods WHERE permission_id = $1 FOR UPDATE`, id); err != nil {
		t.Fatal(err)
	}
	d, err := f.l.Evaluate(ctx, "k1", proposal(id, 1000))
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != engine.Block || !slices.Equal(d.Reasons, []engine.Reason{engine.BudgetConflict}) {
		t.Fatalf("got %s %v", d.Outcome, d.Reasons)
	}
	// The same key works once the lock is free.
	d2, err := f.l.Evaluate(ctx, "k1", proposal(id, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if d2.Outcome != engine.Allow || d2.ID == d.ID {
		t.Fatalf("retry got %s (same id: %v)", d2.Outcome, d2.ID == d.ID)
	}
}

func TestApprove_ConfirmThenApprove(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	d, err := f.l.Evaluate(ctx, "k1", proposal(id, 3000))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != engine.Confirm || d.Status() != "awaiting_approval" || d.Reservation == nil {
		t.Fatalf("got %+v", d)
	}
	f.clock.Advance(10 * time.Minute)
	d, err = f.l.Approve(ctx, d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status() != "ready" || *d.HumanAction != "approved" {
		t.Fatalf("after approve: %s %v", d.Status(), d.HumanAction)
	}
	// Approval restarts the hold's timer.
	if want := f.clock.Now().Add(15 * time.Minute); !d.Reservation.ExpiresAt.Equal(want) {
		t.Errorf("expires %v want %v", d.Reservation.ExpiresAt, want)
	}
	if _, err := f.l.Approve(ctx, d.ID, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("second answer err = %v", err)
	}
}

func TestApprove_DeclineReleases(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	d, _ := f.l.Evaluate(ctx, "k1", proposal(id, 3000))
	d, err := f.l.Approve(ctx, d.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status() != "released" || d.Reservation.State != StateReleased {
		t.Fatalf("got %s", d.Status())
	}
	if b := budget(t, f, id); b.Held != 0 {
		t.Fatalf("held %d", b.Held)
	}
}

func TestApprove_RejectsNonConfirmAndExpired(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	allow, _ := f.l.Evaluate(ctx, "a", proposal(id, 1000))
	if _, err := f.l.Approve(ctx, allow.ID, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("approve ALLOW err = %v", err)
	}
	confirm, _ := f.l.Evaluate(ctx, "c", proposal(id, 3000))
	f.clock.Advance(16 * time.Minute)
	if _, err := f.l.Approve(ctx, confirm.ID, true); !errors.Is(err, ErrExpired) {
		t.Fatalf("approve expired err = %v", err)
	}
	if _, err := f.l.Approve(ctx, "dec_missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestCommitAndRelease(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	a, _ := f.l.Evaluate(ctx, "a", proposal(id, 1000))
	b, _ := f.l.Evaluate(ctx, "b", proposal(id, 2000))
	if err := f.l.Commit(ctx, a.Reservation.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.l.Commit(ctx, a.Reservation.ID); err != nil {
		t.Fatalf("second commit should be a no-op: %v", err)
	}
	if err := f.l.Release(ctx, b.Reservation.ID, ReleasePayFailed); err != nil {
		t.Fatal(err)
	}
	if err := f.l.Release(ctx, b.Reservation.ID, ReleasePayFailed); err != nil {
		t.Fatalf("second release should be a no-op: %v", err)
	}
	if err := f.l.Release(ctx, a.Reservation.ID, ReleasePayFailed); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("releasing committed err = %v", err)
	}
	if got := budget(t, f, id); got.Committed != 1000 || got.Held != 0 {
		t.Fatalf("budget %+v", got)
	}
	// PayPal confirming a capture after the hold lapsed still counts.
	if err := f.l.Commit(ctx, b.Reservation.ID); err != nil {
		t.Fatalf("late commit: %v", err)
	}
	if got := budget(t, f, id); got.Committed != 3000 || got.Held != 0 {
		t.Fatalf("after late commit %+v", got)
	}
}

func TestReleaseExpired(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	old, _ := f.l.Evaluate(ctx, "old", proposal(id, 1000))
	f.clock.Advance(10 * time.Minute)
	fresh, _ := f.l.Evaluate(ctx, "fresh", proposal(id, 1000))
	f.clock.Advance(6 * time.Minute)
	n, err := f.l.ReleaseExpired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("released %d, want 1", n)
	}
	o, _ := f.l.GetDecision(ctx, old.ID)
	fr, _ := f.l.GetDecision(ctx, fresh.ID)
	if o.Reservation.State != StateReleased || *o.Reservation.ReleaseReason != ReleaseExpired || fr.Reservation.State != StateHeld {
		t.Fatalf("old %s fresh %s", o.Reservation.State, fr.Reservation.State)
	}
}

func TestPause_ReleasesHoldsAndBlocks(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	d, _ := f.l.Evaluate(ctx, "a", proposal(id, 1000))
	p, err := f.l.SetStatus(ctx, id, engine.StatusPaused)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusPaused || p.Budget.Held != 0 {
		t.Fatalf("after pause: %s held %d", p.Status, p.Budget.Held)
	}
	got, _ := f.l.GetDecision(ctx, d.ID)
	if got.Reservation.State != StateReleased || *got.Reservation.ReleaseReason != ReleasePaused {
		t.Fatalf("reservation %s", got.Reservation.State)
	}
	blocked, _ := f.l.Evaluate(ctx, "b", proposal(id, 1000))
	if blocked.Outcome != engine.Block || blocked.Explanation != "This permission is paused." {
		t.Fatalf("paused evaluate: %s %q", blocked.Outcome, blocked.Explanation)
	}
	if _, err := f.l.SetStatus(ctx, id, engine.StatusActive); err != nil {
		t.Fatal(err)
	}
	ok, _ := f.l.Evaluate(ctx, "c", proposal(id, 1000))
	if ok.Outcome != engine.Allow {
		t.Fatalf("after resume: %s", ok.Outcome)
	}
}

func TestRevoke_IsFinal(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	if _, err := f.l.SetStatus(ctx, id, engine.StatusRevoked); err != nil {
		t.Fatal(err)
	}
	if _, err := f.l.SetStatus(ctx, id, engine.StatusActive); !errors.Is(err, ErrRevoked) {
		t.Fatalf("resume revoked err = %v", err)
	}
	if _, err := f.l.NewVersion(ctx, id, everyday(f)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("edit revoked err = %v", err)
	}
	d, _ := f.l.Evaluate(ctx, "a", proposal(id, 1000))
	if d.Outcome != engine.Block || d.Explanation != "This permission was revoked." {
		t.Fatalf("revoked evaluate: %s %q", d.Outcome, d.Explanation)
	}
}

func TestNewVersion_AppliesOnlyWhenSigned(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	in := everyday(f)
	in.PerPurchaseCap = 2000
	in.ConfirmAbove = 2000
	p, err := f.l.NewVersion(ctx, id, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.LatestVersion != 2 || *p.CurrentVersion != 1 {
		t.Fatalf("versions latest %d current %d", p.LatestVersion, *p.CurrentVersion)
	}
	before, _ := f.l.Evaluate(ctx, "a", proposal(id, 2500))
	if before.Outcome != engine.Allow || *before.PermissionVersion != 1 {
		t.Fatalf("unsigned edit took effect: %s v%d", before.Outcome, *before.PermissionVersion)
	}
	if _, err := f.l.Sign(ctx, id, 2); err != nil {
		t.Fatal(err)
	}
	after, _ := f.l.Evaluate(ctx, "b", proposal(id, 2500))
	if after.Outcome != engine.Block || *after.PermissionVersion != 2 {
		t.Fatalf("signed edit not applied: %s %v", after.Outcome, after.Reasons)
	}
}

func TestEvaluate_TamperedStoredPermissionBlocks(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	// Someone with database access raises the budget without re-signing.
	if _, err := f.l.pool.Exec(ctx, `UPDATE permission_versions
		SET body = jsonb_set(body, '{monthly_budget_cents}', '1000000') WHERE permission_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	d, err := f.l.Evaluate(ctx, "a", proposal(id, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != engine.Block || d.Explanation != "This permission's signature doesn't match, so I can't use it." {
		t.Fatalf("got %s %q", d.Outcome, d.Explanation)
	}
}

func TestEvaluate_UnknownPermission(t *testing.T) {
	f := setup(t, Config{})
	if _, err := f.l.Evaluate(ctx, "a", proposal("perm_missing", 1000)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestEvents_Timeline(t *testing.T) {
	f := setup(t, Config{})
	id := active(t, f, everyday(f))
	_, _ = f.l.Evaluate(ctx, "a", proposal(id, 1000))
	evs, err := f.l.ListEvents(ctx, id, 10)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	want := []string{"decision", "permission.signed", "permission.created"}
	if !slices.Equal(types, want) {
		t.Fatalf("events %v, want %v", types, want)
	}
}
