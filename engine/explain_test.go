package engine

import (
	"strings"
	"testing"
)

func TestExplain_SentencesMatchReasonTable(t *testing.T) {
	cases := []struct {
		mod  func(*Input)
		want string
	}{
		{nil, "Within your permission: $18 of your $100 monthly budget, 30-day returns."},
		{func(in *Input) { in.Proposal.Amount = 9000 }, "This is above your $40 per-purchase limit."},
		{func(in *Input) { in.Budget.Committed = 9000 }, "This would take you past your $100 monthly limit."},
		{func(in *Input) { in.Proposal.Amount = 3000 }, "Above your $25 approval line, so I'm asking you."},
		{func(in *Input) { in.Proposal.Category = "electronics" }, "Electronics isn't in this permission."},
		{func(in *Input) { in.Proposal.Merchant = "other-shop" }, "First time at this store. Want to allow it?"},
		{func(in *Input) { in.Proposal.ReturnWindowDays = intp(3) }, "Returns are 3 days; your minimum is 14."},
		{func(in *Input) { in.Proposal.ReturnTermsVerified = false }, "I couldn't verify the return policy. Your call."},
		{func(in *Input) { in.Proposal.Currency = "EUR" }, "This store charges in a currency this permission doesn't cover."},
		{func(in *Input) { in.Permission.Status = StatusPaused }, "This permission is paused."},
		{func(in *Input) { in.Proposal.InstructionSource = SourceWeb }, "A web page told the agent to ignore your limits. I didn't."},
	}
	for _, tc := range cases {
		in := input(tc.mod)
		d := mustDecide(t, in)
		if got := Explain(in, d); got != tc.want {
			t.Errorf("reasons %v: got %q want %q", d.Reasons, got, tc.want)
		}
	}
	if got := Explain(Input{}, Conflict()); got != "Another purchase is using this budget right now." {
		t.Errorf("conflict: %q", got)
	}
	if got := ExplainPriceDrift(4800, 6100); got != "The price changed from $48 to $61 at checkout." {
		t.Errorf("drift: %q", got)
	}
}

func TestExplain_NoGuaranteeWords(t *testing.T) {
	banned := []string{"safe", "protected", "guarantee", "insured", "fraud-proof"}
	for _, r := range AllReasons() {
		in := input(nil)
		s := strings.ToLower(Explain(in, Decision{Reasons: []Reason{r}}))
		for _, w := range banned {
			if strings.Contains(s, w) {
				t.Errorf("%s sentence %q uses %q", r, s, w)
			}
		}
	}
}

func TestExplain_BlockReasonIsPrimary(t *testing.T) {
	if Primary([]Reason{NewMerchant, OverPurchaseCap}) != OverPurchaseCap {
		t.Error("block reason should be primary")
	}
}
