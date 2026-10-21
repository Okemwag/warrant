package paypal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// CreateOrder calls POST /v2/checkout/orders.
func (c *Client) CreateOrder(ctx context.Context, requestID string, req CreateOrderRequest) (*Order, error) {
	var o Order
	if err := c.do(ctx, http.MethodPost, "/v2/checkout/orders", requestID, req, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// GetOrder calls GET /v2/checkout/orders/{id}.
func (c *Client) GetOrder(ctx context.Context, id string) (*Order, error) {
	var o Order
	if err := c.do(ctx, http.MethodGet, "/v2/checkout/orders/"+url.PathEscape(id), "", nil, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// AuthorizeOrder calls POST /v2/checkout/orders/{id}/authorize.
func (c *Client) AuthorizeOrder(ctx context.Context, requestID, id string) (*Order, error) {
	var o Order
	if err := c.do(ctx, http.MethodPost, "/v2/checkout/orders/"+url.PathEscape(id)+"/authorize", requestID, struct{}{}, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// CaptureAuthorization calls POST /v2/payments/authorizations/{id}/capture.
func (c *Client) CaptureAuthorization(ctx context.Context, requestID, authID string) (*Capture, error) {
	var cp Capture
	body := map[string]any{"final_capture": true}
	if err := c.do(ctx, http.MethodPost, "/v2/payments/authorizations/"+url.PathEscape(authID)+"/capture", requestID, body, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// VoidAuthorization calls POST /v2/payments/authorizations/{id}/void.
func (c *Client) VoidAuthorization(ctx context.Context, authID string) error {
	return c.do(ctx, http.MethodPost, "/v2/payments/authorizations/"+url.PathEscape(authID)+"/void", "", nil, nil)
}

// CreatePayPalSetupToken calls POST /v3/vault/setup-tokens for a PayPal wallet.
// The buyer must open the returned approve link once.
func (c *Client) CreatePayPalSetupToken(ctx context.Context, requestID, description, returnURL, cancelURL string) (*SetupToken, error) {
	body := map[string]any{
		"payment_source": map[string]any{
			"paypal": map[string]any{
				"description":                    description,
				"usage_pattern":                  "UNSCHEDULED_POSTPAID",
				"usage_type":                     "MERCHANT",
				"customer_type":                  "CONSUMER",
				"permit_multiple_payment_tokens": false,
				"experience_context": map[string]any{
					"return_url":          returnURL,
					"cancel_url":          cancelURL,
					"shipping_preference": "NO_SHIPPING",
					"vault_instruction":   "ON_CREATE_PAYMENT_TOKENS",
				},
			},
		},
	}
	var st SetupToken
	if err := c.do(ctx, http.MethodPost, "/v3/vault/setup-tokens", requestID, body, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// GetSetupToken calls GET /v3/vault/setup-tokens/{id}.
func (c *Client) GetSetupToken(ctx context.Context, id string) (*SetupToken, error) {
	var st SetupToken
	if err := c.do(ctx, http.MethodGet, "/v3/vault/setup-tokens/"+url.PathEscape(id), "", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// CreatePaymentToken calls POST /v3/vault/payment-tokens from an approved setup token.
func (c *Client) CreatePaymentToken(ctx context.Context, requestID, setupTokenID string) (*PaymentToken, error) {
	body := map[string]any{
		"payment_source": map[string]any{
			"token": map[string]any{"id": setupTokenID, "type": "SETUP_TOKEN"},
		},
	}
	var pt PaymentToken
	if err := c.do(ctx, http.MethodPost, "/v3/vault/payment-tokens", requestID, body, &pt); err != nil {
		return nil, err
	}
	return &pt, nil
}

// MerchantInitiated is the stored credential for charging a saved PayPal
// account without the buyer present.
func MerchantInitiated() *StoredCredential {
	return &StoredCredential{PaymentInitiator: "MERCHANT", Usage: "SUBSEQUENT", PaymentType: "UNSCHEDULED"}
}

// GetCapture calls GET /v2/payments/captures/{id}.
func (c *Client) GetCapture(ctx context.Context, captureID string) (*Capture, error) {
	var cp Capture
	if err := c.do(ctx, http.MethodGet, "/v2/payments/captures/"+url.PathEscape(captureID), "", nil, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// Refund is a payments refund.
type Refund struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// RefundCapture calls POST /v2/payments/captures/{id}/refund for the full amount.
func (c *Client) RefundCapture(ctx context.Context, requestID, captureID, note string) (*Refund, error) {
	var rf Refund
	body := map[string]any{}
	if note != "" {
		body["note_to_payer"] = note
	}
	if err := c.do(ctx, http.MethodPost, "/v2/payments/captures/"+url.PathEscape(captureID)+"/refund", requestID, body, &rf); err != nil {
		return nil, err
	}
	return &rf, nil
}

// DeletePaymentToken calls DELETE /v3/vault/payment-tokens/{id}.
func (c *Client) DeletePaymentToken(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v3/vault/payment-tokens/"+url.PathEscape(id), "", nil, nil)
}

// WebhookHeaders are the PayPal transmission headers on a webhook request.
type WebhookHeaders struct {
	AuthAlgo         string `json:"auth_algo"`
	CertURL          string `json:"cert_url"`
	TransmissionID   string `json:"transmission_id"`
	TransmissionSig  string `json:"transmission_sig"`
	TransmissionTime string `json:"transmission_time"`
}

// WebhookHeadersFrom reads the transmission headers from an HTTP request.
func WebhookHeadersFrom(h http.Header) WebhookHeaders {
	return WebhookHeaders{
		AuthAlgo:         h.Get("PAYPAL-AUTH-ALGO"),
		CertURL:          h.Get("PAYPAL-CERT-URL"),
		TransmissionID:   h.Get("PAYPAL-TRANSMISSION-ID"),
		TransmissionSig:  h.Get("PAYPAL-TRANSMISSION-SIG"),
		TransmissionTime: h.Get("PAYPAL-TRANSMISSION-TIME"),
	}
}

// VerifyWebhook calls POST /v1/notifications/verify-webhook-signature and
// reports whether PayPal says the event is genuine. body must be the raw
// request body, unmodified.
func (c *Client) VerifyWebhook(ctx context.Context, webhookID string, h WebhookHeaders, body []byte) (bool, error) {
	req := struct {
		WebhookHeaders
		WebhookID    string          `json:"webhook_id"`
		WebhookEvent json.RawMessage `json:"webhook_event"`
	}{h, webhookID, json.RawMessage(body)}
	var out struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/notifications/verify-webhook-signature", "", req, &out); err != nil {
		return false, err
	}
	return out.VerificationStatus == "SUCCESS", nil
}
