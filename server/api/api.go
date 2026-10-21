// Package api is Warrant's HTTP API.
//
// Two bearer tokens separate the two callers: the console token (the person)
// manages permissions and answers approvals; the agent token can only ask for
// decisions and read them. An agent can never approve its own purchase.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/okemwag/warrant/server/checkout"
	"github.com/okemwag/warrant/server/ledger"
	"github.com/okemwag/warrant/server/merchants"
)

// Role is who is calling.
type Role int

// Roles.
const (
	RoleNone Role = iota
	RoleConsole
	RoleAgent
)

// Config configures the API.
type Config struct {
	ConsoleToken string
	AgentToken   string
	Logger       *slog.Logger
	Now          func() time.Time
	// Checkout is nil when PayPal isn't configured; payment endpoints then
	// answer 503.
	Checkout  *checkout.Service
	Merchants *merchants.Registry
	// Compiler turns plain language into a draft permission. Optional.
	Compiler Compiler
	// Console serves the web app at "/". Optional.
	Console http.Handler
	// ConsoleURL is where PayPal return pages send the person. Default "/".
	ConsoleURL string
}

// Server serves the API.
type Server struct {
	l          *ledger.Ledger
	console    []byte
	agent      []byte
	log        *slog.Logger
	now        func() time.Time
	checkout   *checkout.Service
	merchants  *merchants.Registry
	compiler   Compiler
	consoleURL string
}

// New returns the API handler.
func New(l *ledger.Ledger, cfg Config) (http.Handler, error) {
	if cfg.ConsoleToken == "" || cfg.AgentToken == "" {
		return nil, errors.New("api: WARRANT_CONSOLE_TOKEN and WARRANT_AGENT_TOKEN are required")
	}
	if cfg.ConsoleToken == cfg.AgentToken {
		return nil, errors.New("api: console and agent tokens must differ")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Merchants == nil {
		cfg.Merchants = merchants.Default()
	}
	s := &Server{l: l, console: []byte(cfg.ConsoleToken), agent: []byte(cfg.AgentToken), log: cfg.Logger, now: cfg.Now,
		checkout: cfg.Checkout, merchants: cfg.Merchants, compiler: cfg.Compiler, consoleURL: cfg.ConsoleURL}
	if s.consoleURL == "" {
		s.consoleURL = "/"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	console := s.only(RoleConsole)
	mux.Handle("POST /v1/permissions", console(s.createPermission))
	mux.Handle("GET /v1/permissions", console(s.listPermissions))
	mux.Handle("GET /v1/permissions/{id}", console(s.getPermission))
	mux.Handle("PATCH /v1/permissions/{id}", console(s.setStatus))
	mux.Handle("POST /v1/permissions/{id}/versions", console(s.newVersion))
	mux.Handle("POST /v1/permissions/{id}/sign", console(s.sign))
	mux.Handle("GET /v1/permissions/{id}/decisions", console(s.listDecisions))
	mux.Handle("GET /v1/permissions/{id}/events", console(s.listEvents))
	mux.Handle("POST /v1/decisions/{id}/approve", console(s.approve))

	mux.Handle("GET /v1/config", console(s.config))
	mux.Handle("POST /v1/permissions/draft", console(s.draftPermission))
	mux.Handle("POST /v1/permissions/preview", console(s.previewPermission))
	mux.Handle("GET /v1/permissions/{id}/orders", console(s.listOrders))
	mux.Handle("GET /v1/orders/{id}", console(s.getOrder))
	mux.Handle("POST /v1/orders/{id}/refund", console(s.refund))
	mux.Handle("GET /v1/paypal/connection", console(s.connection))
	mux.Handle("POST /v1/paypal/connect", console(s.connect))
	mux.Handle("POST /v1/paypal/connect/complete", console(s.finishConnect))
	mux.Handle("DELETE /v1/paypal/connection", console(s.disconnect))

	mux.Handle("POST /v1/evaluate", s.only(RoleAgent)(s.evaluate))
	mux.Handle("POST /v1/checkout", s.only(RoleAgent)(s.checkoutHandler))
	mux.Handle("GET /v1/decisions/{id}", s.only(RoleConsole, RoleAgent)(s.getDecision))

	// PayPal calls and redirects. No bearer token: webhooks are verified with
	// PayPal, and return pages only act on orders Warrant created.
	mux.HandleFunc("POST /v1/webhooks/paypal", s.webhook)
	mux.HandleFunc("GET /paypal/return", s.paypalReturn)
	mux.HandleFunc("GET /paypal/cancel", s.paypalCancel)
	mux.HandleFunc("GET /paypal/vault/return", s.vaultReturn)
	mux.HandleFunc("GET /paypal/vault/cancel", s.vaultCancel)

	if cfg.Console != nil {
		mux.Handle("GET /", cfg.Console)
	}

	return s.recover(s.logRequests(mux)), nil
}

func (s *Server) role(r *http.Request) Role {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return RoleNone
	}
	switch {
	case subtle.ConstantTimeCompare([]byte(tok), s.console) == 1:
		return RoleConsole
	case subtle.ConstantTimeCompare([]byte(tok), s.agent) == 1:
		return RoleAgent
	}
	return RoleNone
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// only allows the given roles and turns handler errors into JSON responses.
func (s *Server) only(roles ...Role) func(handlerFunc) http.Handler {
	return func(h handlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := s.role(r)
			if role == RoleNone {
				writeError(w, http.StatusUnauthorized, "unauthorized", "missing or unknown bearer token")
				return
			}
			allowed := false
			for _, want := range roles {
				allowed = allowed || role == want
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "forbidden", "this token cannot call this endpoint")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err := h(w, r); err != nil {
				s.writeErr(w, r, err)
			}
		})
	}
}

// badRequest is a client error with a message safe to show.
type badRequest struct{ msg string }

func (e badRequest) Error() string { return e.msg }

func badf(format string, a ...any) error { return badRequest{fmt.Sprintf(format, a...)} }

// errUnavailable marks a feature that isn't configured on this server.
var errUnavailable = errors.New("unavailable")

func unavailable(msg string) error { return fmt.Errorf("%w: %s", errUnavailable, msg) }

func (s *Server) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var br badRequest
	switch {
	case errors.As(err, &br):
		writeError(w, http.StatusBadRequest, "bad_request", br.msg)
	case errors.Is(err, ledger.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid", cleanMsg(err))
	case errors.Is(err, ledger.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, ledger.ErrRevoked):
		writeError(w, http.StatusConflict, "revoked", "this permission is revoked")
	case errors.Is(err, ledger.ErrExpired):
		writeError(w, http.StatusConflict, "expired", "the hold on this purchase expired; ask the agent to try again")
	case errors.Is(err, ledger.ErrNotHeld):
		writeError(w, http.StatusConflict, "not_held", "this purchase no longer holds budget")
	case errors.Is(err, ledger.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", cleanMsg(err))
	case errors.Is(err, errUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", cleanMsg(err))
	case errors.Is(err, checkout.ErrNoVault):
		writeError(w, http.StatusConflict, "no_paypal_account", "connect a PayPal account first")
	default:
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "something went wrong")
	}
}

func cleanMsg(err error) string {
	msg := err.Error()
	for _, p := range []string{"ledger: invalid input: ", "ledger: state conflict: ", "engine: ", "unavailable: "} {
		msg = strings.ReplaceAll(msg, p, "")
	}
	return msg
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path != "/healthz" {
			s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", v)
				writeError(w, http.StatusInternalServerError, "internal", "something went wrong")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badf("invalid JSON body: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
