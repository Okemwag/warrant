package engine

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"
)

// Budget is the ledger snapshot for the permission's current month.
type Budget struct {
	Committed Cents `json:"committed_cents"`
	Held      Cents `json:"held_cents"`
}

// Spent is committed plus held.
func (b Budget) Spent() Cents { return b.Committed + b.Held }

// Input is everything a decision depends on.
type Input struct {
	Permission SignedPermission
	PublicKey  ed25519.PublicKey
	Budget     Budget
	Proposal   Proposal
	Now        time.Time
	// Extra holds reasons found outside the engine's own checks, such as
	// PRICE_DRIFT at checkout. They are listed first and can only add
	// caution: reasons that would allow are ignored.
	Extra []Reason
}

// InactiveCause says why a permission is inactive.
type InactiveCause string

// Inactive causes.
const (
	CauseNone         InactiveCause = ""
	CauseBadSignature InactiveCause = "signature_invalid"
	CauseDraft        InactiveCause = "draft"
	CausePaused       InactiveCause = "paused"
	CauseRevoked      InactiveCause = "revoked"
	CauseNotStarted   InactiveCause = "not_started"
	CauseExpired      InactiveCause = "expired"
)

// Decision is the engine's answer.
type Decision struct {
	Outcome Outcome  `json:"decision"`
	Reasons []Reason `json:"reasons"`
	// Reserve is the amount the ledger must hold. Zero when blocked.
	Reserve Cents `json:"reserve_cents"`
	// RemainingAfter is the monthly budget left if this purchase goes through.
	RemainingAfter Cents         `json:"remaining_after_cents"`
	Inactive       InactiveCause `json:"inactive_cause,omitempty"`
}

// ErrPermissionMismatch means the proposal names a different permission.
var ErrPermissionMismatch = errors.New("engine: proposal is for a different permission")

// Decide evaluates a proposal. It returns an error only for malformed input;
// every well-formed proposal gets a decision.
//
// Checks run in this order and every failing check adds a reason: signature
// and status, currency, category, merchant, return terms, per-purchase cap,
// monthly budget, approval threshold, instruction source.
func Decide(in Input) (Decision, error) {
	prop := in.Proposal.Normalize()
	if err := prop.Validate(); err != nil {
		return Decision{}, fmt.Errorf("engine: invalid proposal: %w", err)
	}
	perm := in.Permission.Permission
	if prop.PermissionID != perm.ID {
		return Decision{}, ErrPermissionMismatch
	}
	if in.Now.IsZero() {
		return Decision{}, errors.New("engine: Now is required")
	}

	// Without a valid signature and active status, nothing else is trustworthy.
	if cause := inactiveCause(in); cause != CauseNone {
		return Decision{
			Outcome:        Block,
			Reasons:        []Reason{PermissionInactive},
			RemainingAfter: remaining(perm.MonthlyBudget, in.Budget.Spent()),
			Inactive:       cause,
		}, nil
	}
	// Use the normalized form, which is what was signed.
	perm = perm.Normalize()

	var reasons []Reason
	for _, r := range in.Extra {
		if r.Outcome() != Allow {
			reasons = append(reasons, r)
		}
	}
	if prop.Currency != perm.Currency {
		reasons = append(reasons, CurrencyMismatch)
	}
	if !perm.AllowsCategory(prop.Category) {
		reasons = append(reasons, CategoryNotAllowed)
	}
	if !perm.KnowsMerchant(prop.Merchant) {
		if perm.AskOnNewMerchant {
			reasons = append(reasons, NewMerchant)
		} else {
			reasons = append(reasons, MerchantNotAllowed)
		}
	}
	if perm.MinReturnDays > 0 {
		switch {
		case prop.ReturnWindowDays != nil && *prop.ReturnWindowDays < perm.MinReturnDays:
			reasons = append(reasons, ReturnWindowShort)
		case prop.ReturnWindowDays == nil || !prop.ReturnTermsVerified:
			reasons = append(reasons, ReturnTermsUnverified)
		}
	}
	if prop.Amount > perm.PerPurchaseCap {
		reasons = append(reasons, OverPurchaseCap)
	}
	if in.Budget.Spent()+prop.Amount > perm.MonthlyBudget {
		reasons = append(reasons, OverMonthlyBudget)
	}
	if prop.Amount > perm.ConfirmAbove {
		reasons = append(reasons, AboveConfirmThreshold)
	}
	if !prop.InstructionSource.Trusted() || prop.LimitChangeRequested {
		reasons = append(reasons, UntrustedInstruction)
	}
	if len(reasons) == 0 {
		reasons = []Reason{WithinPermission}
	}

	d := Decision{Outcome: Combine(reasons), Reasons: reasons}
	if d.Outcome == Block {
		d.RemainingAfter = remaining(perm.MonthlyBudget, in.Budget.Spent())
	} else {
		d.Reserve = prop.Amount
		d.RemainingAfter = remaining(perm.MonthlyBudget, in.Budget.Spent()+prop.Amount)
	}
	return d, nil
}

// Conflict is the decision when the ledger cannot lock the budget in time.
func Conflict() Decision {
	return Decision{Outcome: Block, Reasons: []Reason{BudgetConflict}}
}

// CheckPrice compares the quoted amount with the final amount PayPal
// authorized. It returns PriceDrift when they differ by more than tolerance.
func CheckPrice(quoted, final, tolerance Cents) (Reason, bool) {
	diff := final - quoted
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		return PriceDrift, true
	}
	return "", false
}

func inactiveCause(in Input) InactiveCause {
	sp := in.Permission
	if sp.Status == StatusDraft {
		return CauseDraft
	}
	if !Verify(sp.Permission, sp.Signature, in.PublicKey) {
		return CauseBadSignature
	}
	switch sp.Status {
	case StatusActive:
	case StatusPaused:
		return CausePaused
	case StatusRevoked:
		return CauseRevoked
	default:
		return CauseRevoked
	}
	p := sp.Permission.Normalize()
	if in.Now.Before(p.StartsAt) {
		return CauseNotStarted
	}
	if !in.Now.Before(p.ExpiresAt) {
		return CauseExpired
	}
	return CauseNone
}

func remaining(budget, spent Cents) Cents {
	if spent >= budget {
		return 0
	}
	return budget - spent
}
