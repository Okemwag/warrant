package api

import (
	"errors"
	"html/template"
	"io"
	"net/http"

	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/engine"
	"github.com/okemwag/warrant/server/checkout"
)

func (s *Server) needCheckout() error {
	if s.checkout == nil {
		return unavailable("PayPal is not configured on this server (set PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET)")
	}
	return nil
}

func (s *Server) checkoutHandler(w http.ResponseWriter, r *http.Request) error {
	if err := s.needCheckout(); err != nil {
		return err
	}
	var body struct {
		DecisionID  string `json:"decision_id"`
		FinalAmount string `json:"final_amount"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.DecisionID == "" {
		return badf("decision_id is required")
	}
	var final *engine.Cents
	if body.FinalAmount != "" {
		c, err := engine.ParseAmount(body.FinalAmount)
		if err != nil {
			return badf("final_amount: %v", err)
		}
		final = &c
	}
	res, err := s.checkout.Checkout(r.Context(), body.DecisionID, final)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) error {
	if s.checkout == nil {
		writeJSON(w, http.StatusOK, map[string]any{"orders": []any{}})
		return nil
	}
	os, err := s.checkout.ListOrders(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": os})
	return nil
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) error {
	if err := s.needCheckout(); err != nil {
		return err
	}
	o, err := s.checkout.GetOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, o)
	return nil
}

func (s *Server) refund(w http.ResponseWriter, r *http.Request) error {
	if err := s.needCheckout(); err != nil {
		return err
	}
	o, err := s.checkout.Refund(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, o)
	return nil
}

func (s *Server) connection(w http.ResponseWriter, r *http.Request) error {
	if s.checkout == nil {
		writeJSON(w, http.StatusOK, checkout.Connection{Status: "unavailable"})
		return nil
	}
	c, err := s.checkout.Connection(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) error {
	if err := s.needCheckout(); err != nil {
		return err
	}
	c, err := s.checkout.Connect(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) finishConnect(w http.ResponseWriter, r *http.Request) error {
	if err := s.needCheckout(); err != nil {
		return err
	}
	c, err := s.checkout.FinishConnect(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) error {
	if err := s.needCheckout(); err != nil {
		return err
	}
	if err := s.checkout.Disconnect(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, checkout.Connection{Status: "none"})
	return nil
}

// webhook receives PayPal events. It acts only on events PayPal verifies.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	if s.checkout == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "PayPal is not configured")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "could not read body")
		return
	}
	err = s.checkout.HandleWebhook(r.Context(), paypal.WebhookHeadersFrom(r.Header), body)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	case errors.Is(err, checkout.ErrWebhookNotConfigured):
		s.log.Warn("webhook received but PAYPAL_WEBHOOK_ID is not set; ignoring")
		writeError(w, http.StatusServiceUnavailable, "unavailable", "webhook verification is not configured")
	case errors.Is(err, checkout.ErrWebhookUnverified):
		s.log.Warn("rejected unverified webhook")
		writeError(w, http.StatusBadRequest, "unverified", "signature did not verify")
	default:
		s.log.Error("webhook", "err", err)
		// 500 makes PayPal retry later.
		writeError(w, http.StatusInternalServerError, "internal", "could not process event")
	}
}

var returnPage = template.Must(template.New("return").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Warrant · {{.Title}}</title>
<style>
:root{--bg:#f7f6f2;--fg:#1c1b19;--muted:#5d5a53;--card:#fff;--line:#e3e0d8;--accent:#1f5f4a}
@media (prefers-color-scheme:dark){:root{--bg:#161614;--fg:#eceae4;--muted:#a9a59b;--card:#1f1e1b;--line:#33312c;--accent:#7cc4a8}}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;padding:16px;box-sizing:border-box}
main{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:28px;max-width:440px}
h1{font-size:20px;margin:0 0 8px}p{color:var(--muted);margin:0 0 16px}a{color:var(--accent);font-weight:600}
.sandbox{font-size:12px;letter-spacing:.04em;text-transform:uppercase;color:var(--muted);margin-bottom:12px}
</style></head><body><main>
<div class="sandbox">PayPal sandbox · no real money</div>
<h1>{{.Title}}</h1><p>{{.Body}}</p><a href="{{.Link}}">Back to Warrant</a>
</main></body></html>`))

func (s *Server) renderReturn(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = returnPage.Execute(w, map[string]string{"Title": title, "Body": body, "Link": s.consoleURL})
}

// paypalReturn is where PayPal sends the buyer after approving (?token=ORDER).
func (s *Server) paypalReturn(w http.ResponseWriter, r *http.Request) {
	if s.checkout == nil {
		s.renderReturn(w, "PayPal isn't configured", "This server has no PayPal credentials.")
		return
	}
	res, err := s.checkout.Complete(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		s.log.Error("paypal return", "err", err)
		s.renderReturn(w, "We couldn't finish the payment", "Warrant will keep checking with PayPal. You can close this tab.")
		return
	}
	switch res.Status {
	case checkout.StatusCompleted:
		s.renderReturn(w, "Payment complete", res.Message+" Your agent can continue.")
	case checkout.StatusPending:
		s.renderReturn(w, "Payment pending", "PayPal accepted the payment and is still processing it.")
	default:
		s.renderReturn(w, "Payment not completed", res.Message)
	}
}

func (s *Server) paypalCancel(w http.ResponseWriter, r *http.Request) {
	if s.checkout != nil {
		if _, err := s.checkout.Cancel(r.Context(), r.URL.Query().Get("token")); err != nil {
			s.log.Warn("paypal cancel", "err", err)
		}
	}
	s.renderReturn(w, "Payment cancelled", "Nothing was charged and the budget hold was released.")
}

func (s *Server) vaultReturn(w http.ResponseWriter, r *http.Request) {
	if s.checkout == nil {
		s.renderReturn(w, "PayPal isn't configured", "This server has no PayPal credentials.")
		return
	}
	c, err := s.checkout.FinishConnect(r.Context())
	if err != nil || c.Status != "connected" {
		s.log.Warn("vault return", "err", err)
		s.renderReturn(w, "PayPal not connected yet", "Warrant couldn't confirm the connection. Try again from the Console.")
		return
	}
	s.renderReturn(w, "PayPal connected", "Purchases Warrant allows can now be paid without sending you to PayPal each time.")
}

func (s *Server) vaultCancel(w http.ResponseWriter, _ *http.Request) {
	s.renderReturn(w, "PayPal not connected", "You cancelled. Each purchase will ask you to approve on PayPal instead.")
}

func (s *Server) config(w http.ResponseWriter, _ *http.Request) error {
	mode := ""
	if s.checkout != nil {
		mode = s.checkout.Mode()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"paypal_configured":   s.checkout != nil,
		"paypal_mode":         mode,
		"compiler_configured": s.compiler != nil,
		"merchants":           s.merchants.All(),
		"sandbox":             true,
	})
	return nil
}
