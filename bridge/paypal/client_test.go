package paypal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNew_RefusesLiveURL(t *testing.T) {
	for _, u := range []string{"https://api-m.paypal.com", "https://api.paypal.com", "http://localhost"} {
		_, err := New(Config{BaseURL: u, ClientID: "a", ClientSecret: "b"})
		if !errors.Is(err, ErrLiveURL) {
			t.Errorf("New(%q) err = %v, want ErrLiveURL", u, err)
		}
	}
	if _, err := New(Config{BaseURL: SandboxBaseURL + "/", ClientID: "a", ClientSecret: "b"}); err != nil {
		t.Errorf("sandbox URL rejected: %v", err)
	}
}

func TestCents_RoundTrip(t *testing.T) {
	cases := map[int64]string{0: "0.00", 5: "0.05", 1800: "18.00", 4899: "48.99", -250: "-2.50"}
	for cents, s := range cases {
		if got := FormatCents(cents); got != s {
			t.Errorf("FormatCents(%d) = %q, want %q", cents, got, s)
		}
		if got, err := ParseCents(s); err != nil || got != cents {
			t.Errorf("ParseCents(%q) = %d, %v; want %d", s, got, err, cents)
		}
	}
	if got, _ := ParseCents("18"); got != 1800 {
		t.Errorf("ParseCents(18) = %d", got)
	}
	if got, _ := ParseCents("18.5"); got != 1850 {
		t.Errorf("ParseCents(18.5) = %d", got)
	}
	if _, err := ParseCents("1.005"); err == nil {
		t.Error("ParseCents(1.005) should fail")
	}
}

// newTestClient points a Client at a fake server, bypassing the sandbox check.
func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{baseURL: srv.URL, clientID: "id", clientSecret: "secret", http: srv.Client()}
}

func TestCreateOrder_SendsRequestIDAndCachesToken(t *testing.T) {
	var tokenCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		if u, p, ok := r.BasicAuth(); !ok || u != "id" || p != "secret" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	})
	mux.HandleFunc("POST /v2/checkout/orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("PayPal-Request-Id") != "dec_1" {
			http.Error(w, "missing request id", http.StatusBadRequest)
			return
		}
		var req CreateOrderRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.PurchaseUnits[0].CustomID != "dec_1" || req.PurchaseUnits[0].Amount.Value != "18.00" {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":"ORD1","status":"CREATED","links":[{"rel":"payer-action","href":"https://sandbox/approve"}]}`))
	})
	c := newTestClient(t, mux)
	req := CreateOrderRequest{Intent: IntentAuthorize, PurchaseUnits: []PurchaseUnit{{CustomID: "dec_1", Amount: USD(1800)}}}
	for i := 0; i < 2; i++ {
		o, err := c.CreateOrder(context.Background(), "dec_1", req)
		if err != nil {
			t.Fatal(err)
		}
		if o.ID != "ORD1" || o.ApproveURL() != "https://sandbox/approve" {
			t.Fatalf("unexpected order %+v", o)
		}
	}
	if tokenCalls.Load() != 1 {
		t.Errorf("token fetched %d times, want 1", tokenCalls.Load())
	}
}

func TestDo_ReturnsAPIErrorWithDebugID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	})
	mux.HandleFunc("GET /v2/checkout/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"name":"RESOURCE_NOT_FOUND","message":"not found","debug_id":"abc123","details":[{"issue":"INVALID_RESOURCE_ID"}]}`))
	})
	c := newTestClient(t, mux)
	_, err := c.GetOrder(context.Background(), "nope")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.DebugID != "abc123" || apiErr.Status != 404 {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "INVALID_RESOURCE_ID") {
		t.Errorf("error text missing issue: %v", err)
	}
}
