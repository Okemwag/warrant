// Package engine is Warrant's decision engine. It is pure: no I/O, no clock,
// no network and no LLM. Every decision is a function of the signed
// permission, the budget snapshot, the proposal and the time passed in.
package engine

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Cents is an amount of money in the smallest currency unit. Never use floats.
type Cents int64

// MaxAmount bounds any single amount so budget sums cannot overflow.
const MaxAmount Cents = 1_000_000_000_00 // $1bn

// String formats cents for people: 4800 -> "$48", 4850 -> "$48.50".
func (c Cents) String() string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	if c%100 == 0 {
		return fmt.Sprintf("%s$%d", sign, c/100)
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

// Status is a permission's lifecycle state.
type Status string

// Statuses. Expiry is derived from ExpiresAt, not stored.
const (
	StatusDraft   Status = "draft"
	StatusActive  Status = "active"
	StatusPaused  Status = "paused"
	StatusRevoked Status = "revoked"
)

// Permission is the set of limits a person signs. Each edit is a new Version.
type Permission struct {
	ID               string    `json:"id"`
	Version          int       `json:"version"`
	Name             string    `json:"name"`
	MonthlyBudget    Cents     `json:"monthly_budget_cents"`
	PerPurchaseCap   Cents     `json:"per_purchase_cap_cents"`
	ConfirmAbove     Cents     `json:"confirm_above_cents"`
	Categories       []string  `json:"categories"`
	Merchants        []string  `json:"merchants"`
	AskOnNewMerchant bool      `json:"ask_on_new_merchant"`
	MinReturnDays    int       `json:"min_return_days"`
	Currency         string    `json:"currency"`
	StartsAt         time.Time `json:"starts_at"`
	ExpiresAt        time.Time `json:"expires_at"`
}

// Normalize returns a copy with lists trimmed, lower-cased, sorted and
// de-duplicated, currency upper-cased and times in UTC to the second.
func (p Permission) Normalize() Permission {
	p.Name = strings.TrimSpace(p.Name)
	p.Categories = normList(p.Categories)
	p.Merchants = normList(p.Merchants)
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	p.StartsAt = p.StartsAt.UTC().Truncate(time.Second)
	p.ExpiresAt = p.ExpiresAt.UTC().Truncate(time.Second)
	return p
}

// Validate reports whether the permission can be signed.
func (p Permission) Validate() error {
	var errs []error
	if p.ID == "" {
		errs = append(errs, errors.New("id is required"))
	}
	if p.Version < 1 {
		errs = append(errs, errors.New("version must be at least 1"))
	}
	if p.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if p.MonthlyBudget <= 0 || p.MonthlyBudget > MaxAmount {
		errs = append(errs, errors.New("monthly budget must be positive"))
	}
	if p.PerPurchaseCap <= 0 || p.PerPurchaseCap > p.MonthlyBudget {
		errs = append(errs, errors.New("per-purchase cap must be positive and no more than the monthly budget"))
	}
	if p.ConfirmAbove < 0 || p.ConfirmAbove > p.PerPurchaseCap {
		errs = append(errs, errors.New("approval threshold must be between 0 and the per-purchase cap"))
	}
	if len(p.Categories) == 0 {
		errs = append(errs, errors.New("at least one category is required"))
	}
	if p.MinReturnDays < 0 || p.MinReturnDays > 365 {
		errs = append(errs, errors.New("minimum return window must be 0 to 365 days"))
	}
	if p.Currency != "USD" {
		errs = append(errs, errors.New("currency must be USD"))
	}
	if p.StartsAt.IsZero() || p.ExpiresAt.IsZero() || !p.ExpiresAt.After(p.StartsAt) {
		errs = append(errs, errors.New("expiry must be after start"))
	}
	return errors.Join(errs...)
}

// canonicalPermission fixes field order (alphabetical) for signing.
// Changing it invalidates every existing signature.
type canonicalPermission struct {
	AskOnNewMerchant bool     `json:"ask_on_new_merchant"`
	Categories       []string `json:"categories"`
	ConfirmAbove     Cents    `json:"confirm_above_cents"`
	Currency         string   `json:"currency"`
	ExpiresAt        string   `json:"expires_at"`
	ID               string   `json:"id"`
	Merchants        []string `json:"merchants"`
	MinReturnDays    int      `json:"min_return_days"`
	MonthlyBudget    Cents    `json:"monthly_budget_cents"`
	Name             string   `json:"name"`
	PerPurchaseCap   Cents    `json:"per_purchase_cap_cents"`
	StartsAt         string   `json:"starts_at"`
	Version          int      `json:"version"`
}

// Canonical returns the exact bytes that get hashed and signed.
func (p Permission) Canonical() []byte {
	n := p.Normalize()
	c := canonicalPermission{
		AskOnNewMerchant: n.AskOnNewMerchant,
		Categories:       nonNil(n.Categories),
		ConfirmAbove:     n.ConfirmAbove,
		Currency:         n.Currency,
		ExpiresAt:        n.ExpiresAt.Format(time.RFC3339),
		ID:               n.ID,
		Merchants:        nonNil(n.Merchants),
		MinReturnDays:    n.MinReturnDays,
		MonthlyBudget:    n.MonthlyBudget,
		Name:             n.Name,
		PerPurchaseCap:   n.PerPurchaseCap,
		StartsAt:         n.StartsAt.Format(time.RFC3339),
		Version:          n.Version,
	}
	b, err := json.Marshal(c)
	if err != nil {
		// Only plain types are marshalled; this cannot fail.
		panic(err)
	}
	return b
}

// Hash is SHA-256 of the canonical bytes.
func (p Permission) Hash() [32]byte {
	return sha256.Sum256(p.Canonical())
}

// AllowsCategory reports whether category is in the permission.
func (p Permission) AllowsCategory(category string) bool {
	return slices.Contains(p.Categories, normItem(category))
}

// KnowsMerchant reports whether merchant is on the list. An empty list means
// any merchant is allowed.
func (p Permission) KnowsMerchant(merchant string) bool {
	return len(p.Merchants) == 0 || slices.Contains(p.Merchants, normItem(merchant))
}

func normItem(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func normList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = normItem(s); s != "" {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
