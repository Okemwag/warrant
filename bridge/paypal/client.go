// Package paypal is a small typed client for the PayPal REST APIs Warrant uses:
// Orders v2, Payments v2 and Payment Tokens v3. It only talks to the sandbox.
package paypal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SandboxBaseURL is the only base URL this client accepts.
const SandboxBaseURL = "https://api-m.sandbox.paypal.com"

// ErrLiveURL is returned when the client is configured for live PayPal.
var ErrLiveURL = errors.New("paypal: only the sandbox is allowed (" + SandboxBaseURL + ")")

// Client calls PayPal with a cached OAuth token.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	http         *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Config configures a Client.
type Config struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

// New returns a Client. It refuses any base URL other than the sandbox.
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = SandboxBaseURL
	}
	if base != SandboxBaseURL {
		return nil, ErrLiveURL
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("paypal: PAYPAL_CLIENT_ID and PAYPAL_CLIENT_SECRET are required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: base, clientID: cfg.ClientID, clientSecret: cfg.ClientSecret, http: hc}, nil
}

// APIError is a non-2xx PayPal response. DebugID is safe to log.
type APIError struct {
	Status  int
	Name    string `json:"name"`
	Message string `json:"message"`
	DebugID string `json:"debug_id"`
	Details []struct {
		Issue       string `json:"issue"`
		Description string `json:"description"`
	} `json:"details"`
}

func (e *APIError) Error() string {
	var issues []string
	for _, d := range e.Details {
		issues = append(issues, d.Issue)
	}
	return fmt.Sprintf("paypal %d %s: %s %v (debug_id=%s)", e.Status, e.Name, e.Message, issues, e.DebugID)
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.clientID, c.clientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("paypal token: status %d: %s", resp.StatusCode, truncate(body))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("paypal token: %w", err)
	}
	c.token = tok.AccessToken
	// Refresh a minute early.
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

// do sends a JSON request. requestID, if set, is sent as PayPal-Request-Id.
func (c *Client) do(ctx context.Context, method, path, requestID string, in, out any) error {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=representation")
	if requestID != "" {
		req.Header.Set("PayPal-Request-Id", requestID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paypal %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, apiErr)
		return apiErr
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("paypal %s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}
