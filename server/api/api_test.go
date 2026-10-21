package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/okemwag/warrant/server/db/testdb"
	"github.com/okemwag/warrant/server/ledger"
	"github.com/okemwag/warrant/server/signer"
)

const (
	consoleTok = "console-test-token"
	agentTok   = "agent-test-token"
)

type client struct {
	t   *testing.T
	srv *httptest.Server
}

func newClient(t *testing.T) *client {
	t.Helper()
	pool := testdb.New(t, "api")
	sig, err := signer.FromBase64Seed(signer.NewSeed())
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(ledger.New(pool, sig, ledger.Config{}), Config{
		ConsoleToken: consoleTok, AgentToken: agentTok,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv}
}

func (c *client) do(method, path, token string, body any, headers ...string) (int, map[string]any) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.srv.URL+path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

var everydayBody = map[string]any{
	"name": "Everyday errands", "monthly_budget_cents": 10000, "per_purchase_cap_cents": 4000,
	"confirm_above_cents": 2500, "categories": []string{"household"}, "merchants": []string{"demo-store"},
	"ask_on_new_merchant": true, "min_return_days": 14,
}

func evaluateBody(permID, amount string) map[string]any {
	return map[string]any{
		"permission_id": permID, "merchant": "demo-store", "category": "household", "amount": amount,
		"currency": "USD", "instruction_source": "user",
	}
}

func (c *client) activePermission() string {
	code, p := c.do("POST", "/v1/permissions", consoleTok, everydayBody)
	if code != http.StatusCreated {
		c.t.Fatalf("create: %d %v", code, p)
	}
	id := p["id"].(string)
	if code, out := c.do("POST", "/v1/permissions/"+id+"/sign", consoleTok, map[string]any{"version": 1}); code != 200 || out["status"] != "active" {
		c.t.Fatalf("sign: %d %v", code, out)
	}
	return id
}

func TestAPI_FullFlow(t *testing.T) {
	c := newClient(t)
	id := c.activePermission()

	// ALLOW
	code, d := c.do("POST", "/v1/evaluate", agentTok, evaluateBody(id, "18.00"), "Idempotency-Key", "k1")
	if code != http.StatusCreated || d["decision"] != "ALLOW" || d["status"] != "ready" {
		t.Fatalf("allow: %d %v", code, d)
	}
	res := d["reservation"].(map[string]any)
	if res["status"] != "held" || res["expires_in_seconds"].(float64) <= 0 {
		t.Fatalf("reservation %v", res)
	}

	// Replay with the same key returns 200 and the same decision.
	code, again := c.do("POST", "/v1/evaluate", agentTok, evaluateBody(id, "18.00"), "Idempotency-Key", "k1")
	if code != http.StatusOK || again["decision_id"] != d["decision_id"] || again["replayed"] != true {
		t.Fatalf("replay: %d %v", code, again)
	}

	// CONFIRM, then the person approves.
	code, conf := c.do("POST", "/v1/evaluate", agentTok, evaluateBody(id, "32.00"), "Idempotency-Key", "k2")
	if code != http.StatusCreated || conf["decision"] != "CONFIRM" || conf["status"] != "awaiting_approval" {
		t.Fatalf("confirm: %d %v", code, conf)
	}
	decID := conf["decision_id"].(string)
	if code, out := c.do("POST", "/v1/decisions/"+decID+"/approve", agentTok, map[string]any{"action": "approve"}); code != http.StatusForbidden {
		t.Fatalf("agent approving its own purchase: %d %v", code, out)
	}
	code, appr := c.do("POST", "/v1/decisions/"+decID+"/approve", consoleTok, map[string]any{"action": "approve"})
	if code != 200 || appr["status"] != "ready" || appr["human_action"] != "approved" {
		t.Fatalf("approve: %d %v", code, appr)
	}
	// The agent sees the approval.
	if code, got := c.do("GET", "/v1/decisions/"+decID, agentTok, nil); code != 200 || got["status"] != "ready" {
		t.Fatalf("agent get: %d %v", code, got)
	}

	// Injection: BLOCK with the shopper sentence.
	inj := evaluateBody(id, "18.00")
	inj["instruction_source"] = "web"
	code, blk := c.do("POST", "/v1/evaluate", agentTok, inj, "Idempotency-Key", "k3")
	if code != http.StatusCreated || blk["decision"] != "BLOCK" || blk["explanation"] != "A web page told the agent to ignore your limits. I didn't." || blk["reservation"] != nil {
		t.Fatalf("injection: %d %v", code, blk)
	}

	// Budget and timeline reflect it all.
	_, p := c.do("GET", "/v1/permissions/"+id, consoleTok, nil)
	if p["budget"].(map[string]any)["held_cents"].(float64) != 5000 || p["remaining_cents"].(float64) != 5000 {
		t.Fatalf("budget %v remaining %v", p["budget"], p["remaining_cents"])
	}
	_, list := c.do("GET", "/v1/permissions/"+id+"/decisions", consoleTok, nil)
	if n := len(list["decisions"].([]any)); n != 3 {
		t.Fatalf("decisions %d, want 3", n)
	}
	_, evs := c.do("GET", "/v1/permissions/"+id+"/events", consoleTok, nil)
	if n := len(evs["events"].([]any)); n < 6 {
		t.Fatalf("events %d", n)
	}

	// Pause releases holds; evaluate is then blocked.
	code, paused := c.do("PATCH", "/v1/permissions/"+id, consoleTok, map[string]any{"status": "paused"})
	if code != 200 || paused["budget"].(map[string]any)["held_cents"].(float64) != 0 {
		t.Fatalf("pause: %d %v", code, paused)
	}
	_, after := c.do("POST", "/v1/evaluate", agentTok, evaluateBody(id, "5.00"), "Idempotency-Key", "k4")
	if after["decision"] != "BLOCK" || after["explanation"] != "This permission is paused." {
		t.Fatalf("after pause: %v", after)
	}
}

func TestAPI_Auth(t *testing.T) {
	c := newClient(t)
	if code, _ := c.do("GET", "/v1/permissions", "", nil); code != http.StatusUnauthorized {
		t.Errorf("no token: %d", code)
	}
	if code, _ := c.do("GET", "/v1/permissions", "wrong", nil); code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := c.do("POST", "/v1/permissions", agentTok, everydayBody); code != http.StatusForbidden {
		t.Errorf("agent creating permission: %d", code)
	}
	if code, _ := c.do("PATCH", "/v1/permissions/x", agentTok, map[string]any{"status": "active"}); code != http.StatusForbidden {
		t.Errorf("agent changing status: %d", code)
	}
	if code, _ := c.do("POST", "/v1/evaluate", consoleTok, evaluateBody("x", "1.00"), "Idempotency-Key", "k"); code != http.StatusForbidden {
		t.Errorf("console evaluating: %d", code)
	}
	if code, _ := c.do("GET", "/healthz", "", nil); code != 200 {
		t.Errorf("healthz: %d", code)
	}
}

func TestAPI_Validation(t *testing.T) {
	c := newClient(t)
	id := c.activePermission()
	cases := []struct {
		name string
		body map[string]any
		hdr  []string
		want int
	}{
		{"missing idempotency key", evaluateBody(id, "1.00"), nil, 400},
		{"bad amount", evaluateBody(id, "1.005"), []string{"Idempotency-Key", "a"}, 400},
		{"negative amount", evaluateBody(id, "-1.00"), []string{"Idempotency-Key", "b"}, 400},
		{"unknown permission", evaluateBody("perm_nope", "1.00"), []string{"Idempotency-Key", "c"}, 404},
		{"unknown field", func() map[string]any { b := evaluateBody(id, "1.00"); b["budget_override"] = 99999; return b }(), []string{"Idempotency-Key", "d"}, 400},
	}
	for _, tc := range cases {
		if code, out := c.do("POST", "/v1/evaluate", agentTok, tc.body, tc.hdr...); code != tc.want {
			t.Errorf("%s: %d %v, want %d", tc.name, code, out, tc.want)
		}
	}
	bad := map[string]any{"name": "x", "monthly_budget_cents": 100, "per_purchase_cap_cents": 500, "categories": []string{"a"}}
	if code, out := c.do("POST", "/v1/permissions", consoleTok, bad); code != 400 {
		t.Errorf("invalid permission: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/v1/permissions/"+id+"/sign", consoleTok, map[string]any{}); code != 400 {
		t.Errorf("sign without version: %d", code)
	}
	if code, _ := c.do("PATCH", "/v1/permissions/"+id, consoleTok, map[string]any{"status": "draft"}); code != 400 {
		t.Errorf("bad status: %d", code)
	}
	if code, _ := c.do("PATCH", "/v1/permissions/"+id, consoleTok, map[string]any{"status": "revoked"}); code != 200 {
		t.Errorf("revoke: %d", code)
	}
	if code, out := c.do("PATCH", "/v1/permissions/"+id, consoleTok, map[string]any{"status": "active"}); code != 409 || out["error"].(map[string]any)["code"] != "revoked" {
		t.Errorf("resume revoked: %d %v", code, out)
	}
}

func TestAPI_NewRequiresDistinctTokens(t *testing.T) {
	if _, err := New(nil, Config{ConsoleToken: "a", AgentToken: "a"}); err == nil {
		t.Error("same tokens accepted")
	}
	if _, err := New(nil, Config{ConsoleToken: "a"}); err == nil {
		t.Error("missing agent token accepted")
	}
}

func TestAPI_ReturnTermsComeFromMerchantData(t *testing.T) {
	c := newClient(t)
	id := c.activePermission()
	// A store Warrant has no data for: the agent's claim of 30 days isn't trusted.
	body := evaluateBody(id, "10.00")
	body["merchant"] = "other-shop"
	body["return_window_days"] = 30
	_, d := c.do("POST", "/v1/evaluate", agentTok, body, "Idempotency-Key", "r1")
	reasons := d["reasons"].([]any)
	if d["decision"] != "CONFIRM" || reasons[0] != "NEW_MERCHANT" || reasons[1] != "RETURN_TERMS_UNVERIFIED" {
		t.Fatalf("unknown store: %v", d)
	}
	// The agent can't claim verification itself.
	body["return_terms_verified"] = true
	if code, _ := c.do("POST", "/v1/evaluate", agentTok, body, "Idempotency-Key", "r2"); code != 400 {
		t.Fatalf("return_terms_verified from agent accepted: %d", code)
	}
}

func TestAPI_PreviewDoesNotStoreAnything(t *testing.T) {
	c := newClient(t)
	code, p := c.do("POST", "/v1/permissions/preview", consoleTok, map[string]any{"permission": everydayBody})
	if code != 200 || p["valid"] != true {
		t.Fatalf("preview: %d %v", code, p)
	}
	trials := p["trials"].([]any)
	var outcomes []string
	for _, tr := range trials {
		outcomes = append(outcomes, tr.(map[string]any)["decision"].(string))
	}
	if len(outcomes) != 3 || outcomes[0] != "ALLOW" || outcomes[1] != "CONFIRM" || outcomes[2] != "BLOCK" {
		t.Fatalf("trial outcomes %v", outcomes)
	}
	if len(p["summary"].([]any)) < 4 {
		t.Fatalf("summary %v", p["summary"])
	}
	_, list := c.do("GET", "/v1/permissions", consoleTok, nil)
	if n := len(list["permissions"].([]any)); n != 0 {
		t.Fatalf("preview stored %d permissions", n)
	}
	bad := map[string]any{"permission": map[string]any{"name": "x", "monthly_budget_cents": 100, "per_purchase_cap_cents": 500, "categories": []string{"a"}}}
	if _, p := c.do("POST", "/v1/permissions/preview", consoleTok, bad); p["valid"] != false || len(p["errors"].([]any)) == 0 {
		t.Fatalf("invalid preview %v", p)
	}
}

func TestAPI_WithoutPayPalOrCompiler(t *testing.T) {
	c := newClient(t)
	id := c.activePermission()
	_, d := c.do("POST", "/v1/evaluate", agentTok, evaluateBody(id, "10.00"), "Idempotency-Key", "x")
	if code, out := c.do("POST", "/v1/checkout", agentTok, map[string]any{"decision_id": d["decision_id"]}); code != 503 {
		t.Fatalf("checkout without PayPal: %d %v", code, out)
	}
	if code, _ := c.do("POST", "/v1/permissions/draft", consoleTok, map[string]any{"text": "up to $50"}); code != 503 {
		t.Fatalf("draft without compiler: %d", code)
	}
	if code, _ := c.do("POST", "/v1/webhooks/paypal", "", map[string]any{"id": "x"}); code != 503 {
		t.Fatalf("webhook without PayPal: %d", code)
	}
	_, cfg := c.do("GET", "/v1/config", consoleTok, nil)
	if cfg["paypal_configured"] != false || cfg["compiler_configured"] != false || cfg["sandbox"] != true {
		t.Fatalf("config %v", cfg)
	}
	if code, _ := c.do("POST", "/v1/checkout", consoleTok, map[string]any{"decision_id": "x"}); code != 403 {
		t.Fatalf("console calling checkout: %d", code)
	}
	if code, _ := c.do("POST", "/v1/paypal/connect", agentTok, nil); code != 403 {
		t.Fatalf("agent connecting PayPal: %d", code)
	}
	if code, _ := c.do("POST", "/v1/orders/x/refund", agentTok, nil); code != 403 {
		t.Fatalf("agent refunding: %d", code)
	}
}
