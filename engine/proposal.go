package engine

import (
	"errors"
	"strings"
)

// Source is where the instruction to buy came from.
type Source string

// Sources. Only the person and the agent acting on the person's task are
// trusted. Anything else, including an unknown value, is untrusted.
const (
	SourceUser     Source = "user"
	SourceAgent    Source = "agent"
	SourceWeb      Source = "web"
	SourceMerchant Source = "merchant"
	SourceTool     Source = "tool_output"
)

// Trusted reports whether a purchase from this source may be allowed.
func (s Source) Trusted() bool {
	return s == SourceUser || s == SourceAgent
}

// Proposal is a normalized purchase request from an agent.
type Proposal struct {
	PermissionID string `json:"permission_id"`
	Merchant     string `json:"merchant"`
	Category     string `json:"category"`
	Amount       Cents  `json:"amount_cents"`
	Currency     string `json:"currency"`
	// ReturnWindowDays is nil when the merchant states no return window.
	ReturnWindowDays *int `json:"return_window_days,omitempty"`
	// ReturnTermsVerified is true only when Warrant checked the terms against
	// structured merchant data, not the agent's word.
	ReturnTermsVerified bool   `json:"return_terms_verified"`
	InstructionSource   Source `json:"instruction_source"`
	// LimitChangeRequested is set when untrusted text tried to change limits.
	LimitChangeRequested bool   `json:"limit_change_requested"`
	Description          string `json:"description,omitempty"`
}

// Normalize trims and lower-cases identifiers and upper-cases currency.
func (p Proposal) Normalize() Proposal {
	p.PermissionID = strings.TrimSpace(p.PermissionID)
	p.Merchant = normItem(p.Merchant)
	p.Category = normItem(p.Category)
	p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
	p.InstructionSource = Source(normItem(string(p.InstructionSource)))
	p.Description = strings.TrimSpace(p.Description)
	return p
}

// Validate rejects proposals the engine cannot judge at all.
func (p Proposal) Validate() error {
	var errs []error
	if p.PermissionID == "" {
		errs = append(errs, errors.New("permission_id is required"))
	}
	if p.Merchant == "" {
		errs = append(errs, errors.New("merchant is required"))
	}
	if p.Category == "" {
		errs = append(errs, errors.New("category is required"))
	}
	if p.Amount <= 0 || p.Amount > MaxAmount {
		errs = append(errs, errors.New("amount must be positive"))
	}
	if len(p.Currency) != 3 {
		errs = append(errs, errors.New("currency must be a 3-letter code"))
	}
	if p.ReturnWindowDays != nil && *p.ReturnWindowDays < 0 {
		errs = append(errs, errors.New("return_window_days cannot be negative"))
	}
	return errors.Join(errs...)
}
