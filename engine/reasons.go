package engine

// Outcome is the engine's answer.
type Outcome string

// Outcomes.
const (
	Allow   Outcome = "ALLOW"
	Confirm Outcome = "CONFIRM"
	Block   Outcome = "BLOCK"
)

// Reason is a machine-readable reason code. The list and wording must match
// the reason-code table in Architecture.md § 6.
type Reason string

// Reason codes, in the order the engine checks them.
const (
	WithinPermission      Reason = "WITHIN_PERMISSION"
	PermissionInactive    Reason = "PERMISSION_INACTIVE"
	CurrencyMismatch      Reason = "CURRENCY_MISMATCH"
	CategoryNotAllowed    Reason = "CATEGORY_NOT_ALLOWED"
	NewMerchant           Reason = "NEW_MERCHANT"
	MerchantNotAllowed    Reason = "MERCHANT_NOT_ALLOWED"
	ReturnWindowShort     Reason = "RETURN_WINDOW_SHORT"
	ReturnTermsUnverified Reason = "RETURN_TERMS_UNVERIFIED"
	OverPurchaseCap       Reason = "OVER_PURCHASE_CAP"
	OverMonthlyBudget     Reason = "OVER_MONTHLY_BUDGET"
	BudgetConflict        Reason = "BUDGET_CONFLICT"
	AboveConfirmThreshold Reason = "ABOVE_CONFIRM_THRESHOLD"
	UntrustedInstruction  Reason = "UNTRUSTED_INSTRUCTION"
	PriceDrift            Reason = "PRICE_DRIFT"
)

var reasonOutcome = map[Reason]Outcome{
	WithinPermission:      Allow,
	PermissionInactive:    Block,
	CurrencyMismatch:      Block,
	CategoryNotAllowed:    Block,
	NewMerchant:           Confirm,
	MerchantNotAllowed:    Block,
	ReturnWindowShort:     Block,
	ReturnTermsUnverified: Confirm,
	OverPurchaseCap:       Block,
	OverMonthlyBudget:     Block,
	BudgetConflict:        Block,
	AboveConfirmThreshold: Confirm,
	UntrustedInstruction:  Block,
	PriceDrift:            Confirm,
}

// Outcome returns the outcome a reason forces on its own.
func (r Reason) Outcome() Outcome {
	if o, ok := reasonOutcome[r]; ok {
		return o
	}
	// An unknown reason must never allow a purchase.
	return Block
}

// AllReasons lists every reason code in check order.
func AllReasons() []Reason {
	return []Reason{
		WithinPermission, PermissionInactive, CurrencyMismatch, CategoryNotAllowed,
		NewMerchant, MerchantNotAllowed, ReturnWindowShort, ReturnTermsUnverified,
		OverPurchaseCap, OverMonthlyBudget, BudgetConflict, AboveConfirmThreshold,
		UntrustedInstruction, PriceDrift,
	}
}

// Combine applies the rule: any BLOCK wins, otherwise any CONFIRM wins,
// otherwise ALLOW.
func Combine(reasons []Reason) Outcome {
	out := Allow
	for _, r := range reasons {
		switch r.Outcome() {
		case Block:
			return Block
		case Confirm:
			out = Confirm
		}
	}
	return out
}
