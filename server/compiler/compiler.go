// Package compiler turns a person's plain-language spending request into a
// draft permission using Claude. The draft is only a suggestion: the person
// edits and signs it, and the engine never sees anything they didn't approve.
//
// Only the person's own words go into the prompt. Merchant pages, tool output
// and other untrusted text never reach the compiler.
package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/ledger"
)

// Draft is a proposed permission plus anything the person should know.
type Draft struct {
	Permission ledger.PermissionInput `json:"permission"`
	// Question is one clarifying question when the request was ambiguous.
	Question string `json:"question,omitempty"`
	// Assumptions lists defaults the compiler filled in.
	Assumptions []string `json:"assumptions"`
}

// Defaults are the "Everyday errands" template values used when the person
// didn't say.
var Defaults = struct {
	Monthly, PerPurchase, ConfirmAbove engine.Cents
	MinReturnDays, DurationDays        int
}{10000, 4000, 2500, 14, 90}

// Model is the raw structured output requested from Claude. Amounts are in
// dollars because that is how people write them; 0 means "not stated".
type Model struct {
	Name             string   `json:"name"`
	MonthlyBudget    float64  `json:"monthly_budget_usd"`
	PerPurchaseCap   float64  `json:"per_purchase_cap_usd"`
	ConfirmAbove     float64  `json:"confirm_above_usd"`
	Categories       []string `json:"categories"`
	Merchants        []string `json:"merchants"`
	AskOnNewMerchant bool     `json:"ask_on_new_merchant"`
	MinReturnDays    int      `json:"min_return_days"`
	DurationDays     int      `json:"duration_days"`
	Question         string   `json:"clarifying_question"`
}

// Schema is the JSON schema for Model (structured outputs).
var Schema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required": []string{"name", "monthly_budget_usd", "per_purchase_cap_usd", "confirm_above_usd", "categories",
		"merchants", "ask_on_new_merchant", "min_return_days", "duration_days", "clarifying_question"},
	"properties": map[string]any{
		"name":                 map[string]any{"type": "string", "description": "Short name for the permission, 2-5 words."},
		"monthly_budget_usd":   map[string]any{"type": "number", "description": "Monthly spending cap in US dollars. 0 if not stated."},
		"per_purchase_cap_usd": map[string]any{"type": "number", "description": "Largest single purchase in US dollars. 0 if not stated."},
		"confirm_above_usd":    map[string]any{"type": "number", "description": "Ask the person before purchases above this many dollars. 0 if not stated."},
		"categories":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Lower-case product categories allowed, e.g. household, groceries, running gear."},
		"merchants":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Lower-case store ids the person named. Empty if any store is fine."},
		"ask_on_new_merchant":  map[string]any{"type": "boolean", "description": "True unless the person said to never use other stores."},
		"min_return_days":      map[string]any{"type": "integer", "description": "Minimum return window in days. 0 if not stated."},
		"duration_days":        map[string]any{"type": "integer", "description": "How many days the permission should last. 0 if not stated."},
		"clarifying_question":  map[string]any{"type": "string", "description": "One short question if something essential is ambiguous, else empty."},
	},
}

// SystemPrompt instructs the model.
const SystemPrompt = `You convert a person's request into spending limits for an AI shopping agent.
Extract only what the person actually said. Do not invent limits they did not state; use 0 or an empty list for anything missing and the app will fill safe defaults the person can review.
If the request asks for something you cannot represent (for example unlimited spending, a currency other than US dollars, or rules about specific products), represent the closest safe limits and ask one short clarifying question.
Amounts are US dollars. Categories are short lower-case nouns. The person will review and edit every field before anything takes effect.`

// LLM is the model call. The Claude implementation is in claude.go.
type LLM interface {
	Complete(ctx context.Context, system, user string, schema map[string]any) ([]byte, error)
}

// Compiler builds drafts.
type Compiler struct {
	llm     LLM
	timeout time.Duration
}

// New returns a Compiler.
func New(llm LLM) *Compiler { return &Compiler{llm: llm, timeout: 45 * time.Second} }

// ErrRefused means the model declined the request.
var ErrRefused = errors.New("compiler: the model declined this request")

// Compile turns text into a draft.
func (c *Compiler) Compile(ctx context.Context, text string, now time.Time) (*Draft, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	raw, err := c.llm.Complete(ctx, SystemPrompt, text, Schema)
	if err != nil {
		return nil, err
	}
	var m Model
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("compiler: model output: %w", err)
	}
	return FromModel(m, now), nil
}

// FromModel turns model output into a draft, filling defaults and noting
// every assumption. It never returns limits looser than what was asked.
func FromModel(m Model, now time.Time) *Draft {
	d := &Draft{Question: strings.TrimSpace(m.Question), Assumptions: []string{}}
	in := ledger.PermissionInput{
		Name:             strings.TrimSpace(m.Name),
		MonthlyBudget:    dollars(m.MonthlyBudget),
		PerPurchaseCap:   dollars(m.PerPurchaseCap),
		ConfirmAbove:     dollars(m.ConfirmAbove),
		Categories:       clean(m.Categories),
		Merchants:        clean(m.Merchants),
		AskOnNewMerchant: m.AskOnNewMerchant || len(clean(m.Merchants)) == 0,
		MinReturnDays:    m.MinReturnDays,
		Currency:         "USD",
		StartsAt:         now.UTC().Truncate(time.Minute),
	}
	note := func(s string) { d.Assumptions = append(d.Assumptions, s) }
	if in.Name == "" {
		in.Name = "My shopping permission"
	}
	if in.MonthlyBudget <= 0 {
		in.MonthlyBudget = Defaults.Monthly
		note(fmt.Sprintf("You didn't give a monthly limit, so I used %s.", in.MonthlyBudget))
	}
	if in.PerPurchaseCap <= 0 {
		in.PerPurchaseCap = min(Defaults.PerPurchase, in.MonthlyBudget)
		note(fmt.Sprintf("You didn't give a per-purchase limit, so I used %s.", in.PerPurchaseCap))
	}
	if in.PerPurchaseCap > in.MonthlyBudget {
		in.PerPurchaseCap = in.MonthlyBudget
		note("The per-purchase limit can't be above the monthly limit, so I lowered it.")
	}
	if in.ConfirmAbove <= 0 {
		in.ConfirmAbove = min(Defaults.ConfirmAbove, in.PerPurchaseCap)
		note(fmt.Sprintf("I'll ask you before anything over %s.", in.ConfirmAbove))
	}
	if in.ConfirmAbove > in.PerPurchaseCap {
		in.ConfirmAbove = in.PerPurchaseCap
	}
	if len(in.Categories) == 0 {
		in.Categories = []string{"household"}
		note("You didn't name what to buy, so I started with household. Change it below.")
	}
	if in.MinReturnDays <= 0 {
		in.MinReturnDays = Defaults.MinReturnDays
		note(fmt.Sprintf("I required at least %d-day returns.", in.MinReturnDays))
	}
	if in.MinReturnDays > 365 {
		in.MinReturnDays = 365
	}
	days := m.DurationDays
	if days <= 0 || days > 366 {
		days = Defaults.DurationDays
		note(fmt.Sprintf("It lasts %d days unless you change it.", days))
	}
	in.ExpiresAt = in.StartsAt.Add(time.Duration(days) * 24 * time.Hour)
	d.Permission = in
	return d
}

func dollars(v float64) engine.Cents {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	c := engine.Cents(math.Round(v * 100))
	if c > engine.MaxAmount {
		return engine.MaxAmount
	}
	return c
}

func clean(items []string) []string {
	out := []string{}
	for _, s := range items {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && len(s) <= 64 {
			out = append(out, s)
		}
	}
	return out
}
