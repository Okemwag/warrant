package engine

import (
	"fmt"
	"strings"
)

// Primary returns the reason the shopper sentence is about: the first BLOCK
// reason, else the first CONFIRM reason, else the first reason.
func Primary(reasons []Reason) Reason {
	for _, want := range []Outcome{Block, Confirm} {
		for _, r := range reasons {
			if r.Outcome() == want {
				return r
			}
		}
	}
	if len(reasons) > 0 {
		return reasons[0]
	}
	return WithinPermission
}

// Explain returns the template shopper sentence for a decision. The LLM
// explainer may reword it, but this is always the fallback.
func Explain(in Input, d Decision) string {
	perm := in.Permission.Permission
	prop := in.Proposal.Normalize()
	switch Primary(d.Reasons) {
	case WithinPermission:
		s := fmt.Sprintf("Within your permission: %s of your %s monthly budget", prop.Amount, perm.MonthlyBudget)
		if prop.ReturnWindowDays != nil {
			s += fmt.Sprintf(", %d-day returns", *prop.ReturnWindowDays)
		}
		return s + "."
	case PermissionInactive:
		switch d.Inactive {
		case CauseBadSignature:
			return "This permission's signature doesn't match, so I can't use it."
		case CauseDraft:
			return "This permission hasn't been signed yet."
		case CauseNotStarted:
			return "This permission hasn't started yet."
		case CauseExpired:
			return "This permission has expired."
		case CauseRevoked:
			return "This permission was revoked."
		default:
			return "This permission is paused."
		}
	case CurrencyMismatch:
		return "This store charges in a currency this permission doesn't cover."
	case CategoryNotAllowed:
		return fmt.Sprintf("%s isn't in this permission.", capitalize(prop.Category))
	case NewMerchant:
		return "First time at this store. Want to allow it?"
	case MerchantNotAllowed:
		return fmt.Sprintf("%s isn't on this permission's store list.", capitalize(prop.Merchant))
	case ReturnWindowShort:
		return fmt.Sprintf("Returns are %d days; your minimum is %d.", deref(prop.ReturnWindowDays), perm.MinReturnDays)
	case ReturnTermsUnverified:
		return "I couldn't verify the return policy. Your call."
	case OverPurchaseCap:
		return fmt.Sprintf("This is above your %s per-purchase limit.", perm.PerPurchaseCap)
	case OverMonthlyBudget:
		return fmt.Sprintf("This would take you past your %s monthly limit.", perm.MonthlyBudget)
	case BudgetConflict:
		return "Another purchase is using this budget right now."
	case AboveConfirmThreshold:
		return fmt.Sprintf("Above your %s approval line, so I'm asking you.", perm.ConfirmAbove)
	case UntrustedInstruction:
		switch prop.InstructionSource {
		case SourceWeb:
			return "A web page told the agent to ignore your limits. I didn't."
		case SourceMerchant:
			return "The store's own text told the agent to buy this. I didn't."
		case SourceTool:
			return "A tool's output told the agent to buy this. I didn't."
		case SourceUser, SourceAgent:
			return "Something tried to change your limits. I didn't."
		default:
			return "I couldn't tell where this purchase request came from, so I didn't buy it."
		}
	}
	return "I stopped this purchase."
}

// ExplainPriceDrift is the sentence for a PRICE_DRIFT re-check.
func ExplainPriceDrift(quoted, final Cents) string {
	return fmt.Sprintf("The price changed from %s to %s at checkout.", quoted, final)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
