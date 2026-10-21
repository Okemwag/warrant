package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"
)

// APIBackend forwards tool calls to the Warrant API with the agent token.
type APIBackend struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewAPIBackend returns an APIBackend.
func NewAPIBackend(baseURL, agentToken string) *APIBackend {
	return &APIBackend{baseURL: strings.TrimRight(baseURL, "/"), token: agentToken, http: &http.Client{Timeout: 60 * time.Second}}
}

// Evaluate calls POST /v1/evaluate.
func (b *APIBackend) Evaluate(ctx context.Context, args map[string]any) (any, error) {
	body := maps.Clone(args)
	key, _ := body["idempotency_key"].(string)
	delete(body, "idempotency_key")
	if key == "" {
		key = randomKey()
	}
	if _, ok := body["currency"]; !ok {
		body["currency"] = "USD"
	}
	return b.post(ctx, "/v1/evaluate", key, body)
}

// Checkout calls POST /v1/checkout.
func (b *APIBackend) Checkout(ctx context.Context, args map[string]any) (any, error) {
	return b.post(ctx, "/v1/checkout", "", args)
}

func (b *APIBackend) post(ctx context.Context, path, key string, body any) (any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("warrant api unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("warrant api returned %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		if e, ok := out["error"].(map[string]any); ok {
			return nil, fmt.Errorf("warrant: %v", e["message"])
		}
		return nil, fmt.Errorf("warrant api returned %d", resp.StatusCode)
	}
	return out, nil
}

func randomKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "mcp-" + hex.EncodeToString(b)
}
