// Package merchants holds structured merchant data Warrant trusts, such as
// return policies. An agent's own claim about return terms is never trusted;
// only a merchant listed here counts as verified.
package merchants

import "strings"

// Merchant is a store Warrant has structured data for.
type Merchant struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ReturnWindowDays int    `json:"return_window_days"`
}

// Registry looks merchants up by ID.
type Registry struct {
	byID map[string]Merchant
}

// Default is the MVP registry: the sandbox demo store. Real merchant-policy
// verification is v1.
func Default() *Registry {
	return New(Merchant{ID: "demo-store", Name: "Warrant Demo Store", ReturnWindowDays: 30})
}

// New builds a registry.
func New(ms ...Merchant) *Registry {
	r := &Registry{byID: map[string]Merchant{}}
	for _, m := range ms {
		r.byID[normalize(m.ID)] = m
	}
	return r
}

// Lookup returns the merchant and whether it is known.
func (r *Registry) Lookup(id string) (Merchant, bool) {
	m, ok := r.byID[normalize(id)]
	return m, ok
}

// All lists the merchants.
func (r *Registry) All() []Merchant {
	out := make([]Merchant, 0, len(r.byID))
	for _, m := range r.byID {
		out = append(out, m)
	}
	return out
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
