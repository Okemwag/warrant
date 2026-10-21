package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/ledger"
)

// defaultPermissionLength applies when a request leaves out expires_at.
const defaultPermissionLength = 90 * 24 * time.Hour

func (s *Server) createPermission(w http.ResponseWriter, r *http.Request) error {
	var in ledger.PermissionInput
	if err := decode(r, &in); err != nil {
		return err
	}
	s.defaultDates(&in)
	p, err := s.l.CreatePermission(r.Context(), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, p)
	return nil
}

func (s *Server) newVersion(w http.ResponseWriter, r *http.Request) error {
	var in ledger.PermissionInput
	if err := decode(r, &in); err != nil {
		return err
	}
	s.defaultDates(&in)
	p, err := s.l.NewVersion(r.Context(), r.PathValue("id"), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, p)
	return nil
}

func (s *Server) defaultDates(in *ledger.PermissionInput) {
	if in.StartsAt.IsZero() {
		in.StartsAt = s.now()
	}
	if in.ExpiresAt.IsZero() {
		in.ExpiresAt = in.StartsAt.Add(defaultPermissionLength)
	}
}

func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) error {
	ps, err := s.l.ListPermissions(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"permissions": ps})
	return nil
}

func (s *Server) getPermission(w http.ResponseWriter, r *http.Request) error {
	p, err := s.l.GetPermission(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) sign(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Version int `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Version < 1 {
		return badf("version is required: sign the version you reviewed")
	}
	p, err := s.l.Sign(r.Context(), r.PathValue("id"), body.Version)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Status engine.Status `json:"status"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	switch body.Status {
	case engine.StatusActive, engine.StatusPaused, engine.StatusRevoked:
	default:
		return badf("status must be active, paused or revoked")
	}
	p, err := s.l.SetStatus(r.Context(), r.PathValue("id"), body.Status)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, p)
	return nil
}

func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) error {
	ds, err := s.l.ListDecisions(r.Context(), r.PathValue("id"), intQuery(r, "limit"))
	if err != nil {
		return err
	}
	out := make([]decisionResponse, len(ds))
	for i := range ds {
		out[i] = s.toResponse(&ds[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
	return nil
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) error {
	evs, err := s.l.ListEvents(r.Context(), r.PathValue("id"), intQuery(r, "limit"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": evs})
	return nil
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Action string `json:"action"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	var approve bool
	switch body.Action {
	case "approve":
		approve = true
	case "decline":
	default:
		return badf(`action must be "approve" or "decline"`)
	}
	d, err := s.l.Approve(r.Context(), r.PathValue("id"), approve)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.toResponse(d))
	return nil
}

func (s *Server) getDecision(w http.ResponseWriter, r *http.Request) error {
	d, err := s.l.GetDecision(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.toResponse(d))
	return nil
}

// evaluateRequest is the agent-facing request (Architecture.md § 10).
type evaluateRequest struct {
	PermissionID       string  `json:"permission_id"`
	Merchant           string  `json:"merchant"`
	Category           string  `json:"category"`
	Amount             string  `json:"amount"`
	Currency           string  `json:"currency"`
	ReturnWindowDays   *int    `json:"return_window_days"`
	InstructionSource  string  `json:"instruction_source"`
	LimitChangeRequest bool    `json:"limit_change_requested"`
	Description        string  `json:"description"`
	IdempotencyKey     *string `json:"idempotency_key"`
}

func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) error {
	var req evaluateRequest
	if err := decode(r, &req); err != nil {
		return err
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" && req.IdempotencyKey != nil {
		key = strings.TrimSpace(*req.IdempotencyKey)
	}
	if key == "" || len(key) > 200 {
		return badf("an Idempotency-Key header (1-200 characters) is required")
	}
	amount, err := engine.ParseAmount(req.Amount)
	if err != nil {
		return badf("%v", err)
	}
	prop := engine.Proposal{
		PermissionID: req.PermissionID, Merchant: req.Merchant, Category: req.Category,
		Amount: amount, Currency: req.Currency, ReturnWindowDays: req.ReturnWindowDays,
		InstructionSource:    engine.Source(req.InstructionSource),
		LimitChangeRequested: req.LimitChangeRequest, Description: req.Description,
	}
	// Return terms count as verified only from Warrant's own merchant data,
	// never from the agent's word.
	if m, ok := s.merchants.Lookup(req.Merchant); ok {
		days := m.ReturnWindowDays
		prop.ReturnWindowDays = &days
		prop.ReturnTermsVerified = true
	}
	d, err := s.l.Evaluate(r.Context(), key, prop)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if !d.Replayed {
		status = http.StatusCreated
	}
	writeJSON(w, status, s.toResponse(d))
	return nil
}

type reservationResponse struct {
	ID               string       `json:"id"`
	Status           string       `json:"status"`
	Amount           engine.Cents `json:"amount_cents"`
	ExpiresAt        time.Time    `json:"expires_at"`
	ExpiresInSeconds int64        `json:"expires_in_seconds"`
	ReleaseReason    *string      `json:"release_reason,omitempty"`
}

type decisionResponse struct {
	DecisionID        string               `json:"decision_id"`
	Decision          engine.Outcome       `json:"decision"`
	Status            string               `json:"status"`
	Reasons           []engine.Reason      `json:"reasons"`
	Explanation       string               `json:"explanation"`
	PermissionID      string               `json:"permission_id"`
	PermissionVersion *int                 `json:"permission_version"`
	Merchant          string               `json:"merchant"`
	Category          string               `json:"category"`
	Amount            engine.Cents         `json:"amount_cents"`
	RemainingAfter    engine.Cents         `json:"remaining_after_cents"`
	InstructionSource engine.Source        `json:"instruction_source"`
	HumanAction       *string              `json:"human_action,omitempty"`
	Reservation       *reservationResponse `json:"reservation"`
	CreatedAt         time.Time            `json:"created_at"`
	Replayed          bool                 `json:"replayed,omitempty"`
}

func (s *Server) toResponse(d *ledger.DecisionRecord) decisionResponse {
	out := decisionResponse{
		DecisionID: d.ID, Decision: d.Outcome, Status: d.Status(), Reasons: d.Reasons, Explanation: d.Explanation,
		PermissionID: d.PermissionID, PermissionVersion: d.PermissionVersion,
		Merchant: d.Proposal.Merchant, Category: d.Proposal.Category, Amount: d.Amount, RemainingAfter: d.Remaining,
		InstructionSource: d.Proposal.InstructionSource, HumanAction: d.HumanAction, CreatedAt: d.CreatedAt, Replayed: d.Replayed,
	}
	if res := d.Reservation; res != nil {
		left := int64(res.ExpiresAt.Sub(s.now()).Seconds())
		if res.State != ledger.StateHeld || left < 0 {
			left = 0
		}
		out.Reservation = &reservationResponse{
			ID: res.ID, Status: res.State, Amount: res.Amount, ExpiresAt: res.ExpiresAt,
			ExpiresInSeconds: left, ReleaseReason: res.ReleaseReason,
		}
	}
	return out
}

func intQuery(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(name))
	return n
}
