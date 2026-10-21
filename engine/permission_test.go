package engine

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestCanonical_StableAcrossInputOrder(t *testing.T) {
	a := everyday()
	b := everyday()
	b.Categories = []string{"GROCERIES", " household ", "groceries"}
	b.Currency = "USD"
	b.StartsAt = a.StartsAt.In(time.FixedZone("EAT", 3*3600)).Add(500 * time.Millisecond)
	if !bytes.Equal(a.Canonical(), b.Canonical()) {
		t.Fatalf("canonical differs:\n%s\n%s", a.Canonical(), b.Canonical())
	}
}

func TestCanonical_ChangesWithEveryField(t *testing.T) {
	base := everyday().Canonical()
	mods := []func(*Permission){
		func(p *Permission) { p.ID = "x" },
		func(p *Permission) { p.Version = 2 },
		func(p *Permission) { p.Name = "x" },
		func(p *Permission) { p.MonthlyBudget++ },
		func(p *Permission) { p.PerPurchaseCap++ },
		func(p *Permission) { p.ConfirmAbove++ },
		func(p *Permission) { p.Categories = append(p.Categories, "toys") },
		func(p *Permission) { p.Merchants = nil },
		func(p *Permission) { p.AskOnNewMerchant = false },
		func(p *Permission) { p.MinReturnDays = 7 },
		func(p *Permission) { p.Currency = "EUR" },
		func(p *Permission) { p.StartsAt = p.StartsAt.Add(time.Hour) },
		func(p *Permission) { p.ExpiresAt = p.ExpiresAt.Add(time.Hour) },
	}
	for i, mod := range mods {
		p := everyday()
		mod(&p)
		if bytes.Equal(base, p.Canonical()) {
			t.Errorf("modification %d did not change canonical bytes", i)
		}
	}
}

func TestCanonical_FieldOrderIsAlphabetical(t *testing.T) {
	s := string(everyday().Canonical())
	keys := []string{"ask_on_new_merchant", "categories", "confirm_above_cents", "currency", "expires_at", "id", "merchants", "min_return_days", "monthly_budget_cents", "name", "per_purchase_cap_cents", "starts_at", "version"}
	last := -1
	for _, k := range keys {
		i := strings.Index(s, `"`+k+`"`)
		if i <= last {
			t.Fatalf("key %s out of order in %s", k, s)
		}
		last = i
	}
}

func TestValidate(t *testing.T) {
	if err := everyday().Normalize().Validate(); err != nil {
		t.Fatalf("valid permission rejected: %v", err)
	}
	bad := []func(*Permission){
		func(p *Permission) { p.Name = "" },
		func(p *Permission) { p.MonthlyBudget = 0 },
		func(p *Permission) { p.PerPurchaseCap = p.MonthlyBudget + 1 },
		func(p *Permission) { p.ConfirmAbove = p.PerPurchaseCap + 1 },
		func(p *Permission) { p.Categories = nil },
		func(p *Permission) { p.Currency = "EUR" },
		func(p *Permission) { p.ExpiresAt = p.StartsAt },
		func(p *Permission) { p.MinReturnDays = -1 },
		func(p *Permission) { p.Version = 0 },
	}
	for i, mod := range bad {
		p := everyday()
		mod(&p)
		if err := p.Normalize().Validate(); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestCentsString(t *testing.T) {
	for c, want := range map[Cents]string{4800: "$48", 4850: "$48.50", 5: "$0.05", 0: "$0", -250: "-$2.50"} {
		if got := c.String(); got != want {
			t.Errorf("%d: got %q want %q", c, got, want)
		}
	}
}
