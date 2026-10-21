package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type fakeBackend struct{ evalArgs map[string]any }

func (f *fakeBackend) Evaluate(_ context.Context, args map[string]any) (any, error) {
	f.evalArgs = args
	return map[string]any{"decision": "ALLOW", "decision_id": "dec_1"}, nil
}

func (f *fakeBackend) Checkout(context.Context, map[string]any) (any, error) {
	return nil, errors.New("PayPal is not configured")
}

func run(t *testing.T, b Backend, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := NewServer(b, "test").Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	dec := json.NewDecoder(&out)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		res = append(res, m)
	}
	return res
}

func TestToolsAreExactlyEvaluateAndCheckout(t *testing.T) {
	var names []string
	for _, tool := range Tools() {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"evaluate", "checkout"}) {
		t.Fatalf("tools = %v; the agent must get exactly evaluate and checkout", names)
	}
}

func TestProtocol(t *testing.T) {
	fb := &fakeBackend{}
	res := run(t, fb,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"evaluate","arguments":{"permission_id":"perm_1","amount":"18.00"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"checkout","arguments":{"decision_id":"dec_1"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"create_order","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"nope"}`,
		`not json`,
	)
	if len(res) != 7 {
		t.Fatalf("got %d responses (notification must not be answered): %v", len(res), res)
	}
	init := res[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || init["serverInfo"].(map[string]any)["name"] != "warrant" {
		t.Fatalf("initialize %v", init)
	}
	if n := len(res[1]["result"].(map[string]any)["tools"].([]any)); n != 2 {
		t.Fatalf("tools/list returned %d tools", n)
	}
	call := res[2]["result"].(map[string]any)
	if call["isError"] != false || call["structuredContent"].(map[string]any)["decision"] != "ALLOW" || fb.evalArgs["permission_id"] != "perm_1" {
		t.Fatalf("evaluate call %v", call)
	}
	if res[3]["result"].(map[string]any)["isError"] != true {
		t.Fatalf("backend error should be a tool error: %v", res[3])
	}
	if res[4]["error"].(map[string]any)["code"].(float64) != -32602 {
		t.Fatalf("unknown tool: %v", res[4])
	}
	if res[5]["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", res[5])
	}
	if res[6]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("parse error: %v", res[6])
	}
}

func TestAPIBackend_SendsAgentTokenAndIdempotencyKey(t *testing.T) {
	var got http.Header
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"decision":"CONFIRM","decision_id":"dec_2"}`))
	}))
	defer srv.Close()
	b := NewAPIBackend(srv.URL, "agent-tok")
	out, err := b.Evaluate(context.Background(), map[string]any{"permission_id": "p", "idempotency_key": "k-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer agent-tok" || got.Get("Idempotency-Key") != "k-1" {
		t.Fatalf("headers %v", got)
	}
	if _, leaked := body["idempotency_key"]; leaked || body["currency"] != "USD" {
		t.Fatalf("body %v", body)
	}
	if out.(map[string]any)["decision"] != "CONFIRM" {
		t.Fatalf("out %v", out)
	}
}

func TestAPIBackend_SurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"this token cannot call this endpoint"}}`))
	}))
	defer srv.Close()
	_, err := NewAPIBackend(srv.URL, "x").Checkout(context.Background(), map[string]any{"decision_id": "d"})
	if err == nil || !strings.Contains(err.Error(), "cannot call") {
		t.Fatalf("err = %v", err)
	}
}
