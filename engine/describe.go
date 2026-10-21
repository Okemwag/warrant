package engine

import (
	"fmt"
	"strings"
)

// Describe is the plain-language preview a person reads before signing. It is
// a template, not an LLM, so it always says exactly what the engine enforces.
func Describe(p Permission) []string {
	p = p.Normalize()
	lines := []string{
		fmt.Sprintf("Your agent can spend up to %s a month, and at most %s on any one purchase.", p.MonthlyBudget, p.PerPurchaseCap),
	}
	if p.ConfirmAbove < p.PerPurchaseCap {
		lines = append(lines, fmt.Sprintf("Anything over %s waits for your OK.", p.ConfirmAbove))
	} else {
		lines = append(lines, "Purchases within these limits go through without asking you.")
	}
	lines = append(lines, fmt.Sprintf("Only for: %s.", humanList(p.Categories)))
	switch {
	case len(p.Merchants) == 0:
		lines = append(lines, "Any store.")
	case p.AskOnNewMerchant:
		lines = append(lines, fmt.Sprintf("Stores: %s. A new store needs your OK.", humanList(p.Merchants)))
	default:
		lines = append(lines, fmt.Sprintf("Only these stores: %s.", humanList(p.Merchants)))
	}
	if p.MinReturnDays > 0 {
		lines = append(lines, fmt.Sprintf("Only items with at least %d days to return. If I can't check the return policy, I'll ask you.", p.MinReturnDays))
	}
	lines = append(lines, fmt.Sprintf("Valid %s to %s (UTC). You can pause or revoke it any time.",
		p.StartsAt.Format("Jan 2, 2006"), p.ExpiresAt.Format("Jan 2, 2006")))
	return lines
}

func humanList(items []string) string {
	switch len(items) {
	case 0:
		return "nothing"
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
