package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"time"

	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/compiler"
	"github.com/okemwag/warrant/server/ledger"
)

// Compiler turns a person's words into a draft permission for review.
type Compiler interface {
	Compile(ctx context.Context, text string, now time.Time) (*compiler.Draft, error)
}

func (s *Server) draftPermission(w http.ResponseWriter, r *http.Request) error {
	if s.compiler == nil {
		return unavailable("the plain-language compiler needs ANTHROPIC_API_KEY; pick a template instead")
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || len(text) > 2000 {
		return badf("text must be 1-2000 characters")
	}
	draft, err := s.compiler.Compile(r.Context(), text, s.now())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": draft, "preview": s.preview(draft.Permission, nil)})
	return nil
}

type trialProposal struct {
	Label             string        `json:"label"`
	Merchant          string        `json:"merchant"`
	Category          string        `json:"category"`
	Amount            engine.Cents  `json:"amount_cents"`
	InstructionSource engine.Source `json:"instruction_source"`
}

type trialResult struct {
	trialProposal
	Decision    engine.Outcome  `json:"decision"`
	Reasons     []engine.Reason `json:"reasons"`
	Explanation string          `json:"explanation"`
}

type previewResponse struct {
	Summary []string      `json:"summary"`
	Valid   bool          `json:"valid"`
	Errors  []string      `json:"errors"`
	Trials  []trialResult `json:"trials"`
}

// previewPermission shows how an unsigned permission would read and decide,
// without storing or reserving anything.
func (s *Server) previewPermission(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Permission ledger.PermissionInput `json:"permission"`
		Trials     []trialProposal        `json:"trials"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.preview(body.Permission, body.Trials))
	return nil
}

func (s *Server) preview(in ledger.PermissionInput, trials []trialProposal) previewResponse {
	s.defaultDates(&in)
	p := in.Engine("perm_preview", 1)
	out := previewResponse{Summary: engine.Describe(p), Errors: []string{}, Trials: []trialResult{}}
	if err := p.Validate(); err != nil {
		out.Errors = append(out.Errors, strings.Split(err.Error(), "\n")...)
		return out
	}
	out.Valid = true
	if len(trials) == 0 {
		trials = defaultTrials(p)
	}
	// Sign with a throwaway key: this never leaves the request.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sp := engine.SignedPermission{Permission: p, Status: engine.StatusActive, Signature: engine.Sign(p, priv)}
	for _, tr := range trials {
		prop := engine.Proposal{
			PermissionID: p.ID, Merchant: tr.Merchant, Category: tr.Category, Amount: tr.Amount,
			Currency: "USD", InstructionSource: tr.InstructionSource,
		}
		if prop.InstructionSource == "" {
			prop.InstructionSource = engine.SourceUser
		}
		if m, ok := s.merchants.Lookup(tr.Merchant); ok {
			days := m.ReturnWindowDays
			prop.ReturnWindowDays, prop.ReturnTermsVerified = &days, true
		}
		in := engine.Input{Permission: sp, PublicKey: pub, Proposal: prop, Now: p.StartsAt.Add(time.Minute)}
		d, err := engine.Decide(in)
		if err != nil {
			continue
		}
		out.Trials = append(out.Trials, trialResult{trialProposal: tr, Decision: d.Outcome, Reasons: d.Reasons, Explanation: engine.Explain(in, d)})
	}
	return out
}

// defaultTrials are the three first-run purchases: one that fits, one that
// needs approval and one that is blocked.
func defaultTrials(p engine.Permission) []trialProposal {
	merchant := "demo-store"
	if len(p.Merchants) > 0 {
		merchant = p.Merchants[0]
	}
	cat := p.Categories[0]
	fits := max(p.ConfirmAbove/2, 100)
	if fits > p.ConfirmAbove && p.ConfirmAbove > 0 {
		fits = p.ConfirmAbove
	}
	trials := []trialProposal{{Label: "Fits your limits", Merchant: merchant, Category: cat, Amount: roundDollars(fits), InstructionSource: engine.SourceUser}}
	if p.ConfirmAbove < p.PerPurchaseCap {
		asks := p.ConfirmAbove + (p.PerPurchaseCap-p.ConfirmAbove)/2
		trials = append(trials, trialProposal{Label: "Needs your OK", Merchant: merchant, Category: cat, Amount: roundDollars(asks), InstructionSource: engine.SourceUser})
	} else {
		trials = append(trials, trialProposal{Label: "Needs your OK", Merchant: "new-shop", Category: cat, Amount: roundDollars(fits), InstructionSource: engine.SourceUser})
	}
	trials = append(trials, trialProposal{Label: "A web page tries to raise your limit", Merchant: merchant, Category: cat, Amount: roundDollars(fits), InstructionSource: engine.SourceWeb})
	return trials
}

func roundDollars(c engine.Cents) engine.Cents {
	if c >= 100 {
		return c / 100 * 100
	}
	return c
}
