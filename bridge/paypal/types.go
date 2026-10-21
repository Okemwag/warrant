package paypal

import (
	"fmt"
	"strconv"
	"strings"
)

// Money is PayPal's amount object. Value is a decimal string such as "18.00".
type Money struct {
	CurrencyCode string `json:"currency_code"`
	Value        string `json:"value"`
}

// USD builds a Money from integer cents.
func USD(cents int64) Money {
	return Money{CurrencyCode: "USD", Value: FormatCents(cents)}
}

// FormatCents turns 1800 into "18.00".
func FormatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// ParseCents turns "18.00" or "18" into 1800. It rejects more than two decimals.
func ParseCents(v string) (int64, error) {
	whole, frac, _ := strings.Cut(v, ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("paypal: amount %q has more than two decimals", v)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("paypal: amount %q: %w", v, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("paypal: amount %q: %w", v, err)
	}
	if strings.HasPrefix(whole, "-") {
		return w*100 - f, nil
	}
	return w*100 + f, nil
}

// Link is a HATEOAS link.
type Link struct {
	Href   string `json:"href"`
	Rel    string `json:"rel"`
	Method string `json:"method"`
}

// LinkHref returns the href for rel, or "".
func LinkHref(links []Link, rel string) string {
	for _, l := range links {
		if l.Rel == rel {
			return l.Href
		}
	}
	return ""
}

// Intent values for orders.
const (
	IntentCapture   = "CAPTURE"
	IntentAuthorize = "AUTHORIZE"
)

// Order statuses (Orders v2).
const (
	OrderCreated              = "CREATED"
	OrderApproved             = "APPROVED"
	OrderCompleted            = "COMPLETED"
	OrderPayerActionRequired  = "PAYER_ACTION_REQUIRED"
	OrderVoided               = "VOIDED"
	AuthorizationCreated      = "CREATED"
	AuthorizationCaptured     = "CAPTURED"
	CaptureCompleted          = "COMPLETED"
	SetupTokenApproved        = "APPROVED"
	SetupTokenPayerActionReqd = "PAYER_ACTION_REQUIRED"
)

// PurchaseUnit is one purchase unit in an order request.
type PurchaseUnit struct {
	ReferenceID string `json:"reference_id,omitempty"`
	CustomID    string `json:"custom_id,omitempty"`
	InvoiceID   string `json:"invoice_id,omitempty"`
	Description string `json:"description,omitempty"`
	Amount      Money  `json:"amount"`
}

// ExperienceContext controls the PayPal approval page.
type ExperienceContext struct {
	BrandName          string `json:"brand_name,omitempty"`
	ShippingPreference string `json:"shipping_preference,omitempty"`
	UserAction         string `json:"user_action,omitempty"`
	ReturnURL          string `json:"return_url,omitempty"`
	CancelURL          string `json:"cancel_url,omitempty"`
}

// StoredCredential describes a merchant-initiated charge on a saved account.
type StoredCredential struct {
	PaymentInitiator string `json:"payment_initiator"`
	Usage            string `json:"usage,omitempty"`
	PaymentType      string `json:"payment_type,omitempty"`
}

// PayPalSource is payment_source.paypal.
type PayPalSource struct {
	VaultID           string             `json:"vault_id,omitempty"`
	StoredCredential  *StoredCredential  `json:"stored_credential,omitempty"`
	ExperienceContext *ExperienceContext `json:"experience_context,omitempty"`
}

// PaymentSource is the payment_source object.
type PaymentSource struct {
	PayPal *PayPalSource `json:"paypal,omitempty"`
}

// CreateOrderRequest is the body of POST /v2/checkout/orders.
type CreateOrderRequest struct {
	Intent        string         `json:"intent"`
	PurchaseUnits []PurchaseUnit `json:"purchase_units"`
	PaymentSource *PaymentSource `json:"payment_source,omitempty"`
}

// Authorization is a payments authorization inside an order.
type Authorization struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Amount Money  `json:"amount"`
}

// Capture is a payments capture.
type Capture struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Amount Money  `json:"amount"`
}

// Order is the subset of the order representation Warrant reads.
type Order struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Intent        string `json:"intent"`
	Links         []Link `json:"links"`
	PurchaseUnits []struct {
		ReferenceID string `json:"reference_id"`
		CustomID    string `json:"custom_id"`
		InvoiceID   string `json:"invoice_id"`
		Amount      Money  `json:"amount"`
		Payments    struct {
			Authorizations []Authorization `json:"authorizations"`
			Captures       []Capture       `json:"captures"`
		} `json:"payments"`
	} `json:"purchase_units"`
}

// ApproveURL returns the buyer approval link, if any.
func (o *Order) ApproveURL() string {
	if h := LinkHref(o.Links, "payer-action"); h != "" {
		return h
	}
	return LinkHref(o.Links, "approve")
}

// FirstAuthorization returns the first authorization on the order, or nil.
func (o *Order) FirstAuthorization() *Authorization {
	for _, pu := range o.PurchaseUnits {
		if len(pu.Payments.Authorizations) > 0 {
			return &pu.Payments.Authorizations[0]
		}
	}
	return nil
}

// SetupToken is a Vault v3 setup token.
type SetupToken struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Links  []Link `json:"links"`
}

// PaymentToken is a Vault v3 payment token (the vault_id).
type PaymentToken struct {
	ID string `json:"id"`
}
