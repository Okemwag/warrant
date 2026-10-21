// Command paypal runs week-one gate 1: can we authorize and capture a PayPal
// sandbox order?
//
//	go run ./scripts/gates/paypal -mode approve   # Mode A: buyer approves the order
//	go run ./scripts/gates/paypal -mode vault     # Mode B: save PayPal once, then pay with no redirect
//
// Both modes need a sandbox *personal* (buyer) account to log in with when
// the script prints an approval link.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/internal/config"
)

const (
	returnURL = "https://example.com/warrant/return"
	cancelURL = "https://example.com/warrant/cancel"
	vaultFile = ".gate1-vault-id.local" // gitignored
)

func main() {
	mode := flag.String("mode", "approve", "approve (Mode A) or vault (Mode B)")
	amount := flag.Int64("cents", 100, "amount to charge in cents")
	vaultID := flag.String("vault-id", "", "Mode B: reuse a payment token and skip the one-time approval (default: read "+vaultFile+")")
	fresh := flag.Bool("fresh", false, "Mode B: ignore the saved payment token and approve again")
	wait := flag.Duration("wait", 10*time.Minute, "how long to wait for buyer approval")
	flag.Parse()

	_ = config.LoadDotEnv(".env")
	client, err := paypal.New(paypal.Config{
		BaseURL:      os.Getenv("PAYPAL_BASE_URL"),
		ClientID:     os.Getenv("PAYPAL_CLIENT_ID"),
		ClientSecret: os.Getenv("PAYPAL_CLIENT_SECRET"),
	})
	if err != nil {
		fail(err)
	}

	if *mode == "vault" && *vaultID == "" && !*fresh {
		if b, err := os.ReadFile(vaultFile); err == nil {
			*vaultID = string(b)
		}
	}

	ctx := context.Background()
	switch *mode {
	case "approve":
		err = modeA(ctx, client, *amount, *wait)
	case "vault":
		err = modeB(ctx, client, *amount, *vaultID, *wait)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		fail(err)
	}
	fmt.Printf("\nGATE 1 PASS (mode %s)\n", *mode)
}

func modeA(ctx context.Context, c *paypal.Client, cents int64, wait time.Duration) error {
	id := "gate1-" + randHex()
	step("Create order (intent AUTHORIZE, custom_id=%s)", id)
	order, err := c.CreateOrder(ctx, id, paypal.CreateOrderRequest{
		Intent:        paypal.IntentAuthorize,
		PurchaseUnits: []paypal.PurchaseUnit{{CustomID: id, InvoiceID: id, Description: "Warrant gate 1", Amount: paypal.USD(cents)}},
		PaymentSource: &paypal.PaymentSource{PayPal: &paypal.PayPalSource{ExperienceContext: &paypal.ExperienceContext{
			BrandName: "Warrant Demo Store", ShippingPreference: "NO_SHIPPING", UserAction: "CONTINUE",
			ReturnURL: returnURL, CancelURL: cancelURL,
		}}},
	})
	if err != nil {
		return err
	}
	ok("order %s status %s", order.ID, order.Status)

	fmt.Printf("\n  Open this link, log in with your sandbox BUYER account and approve:\n  %s\n\n", order.ApproveURL())
	step("Waiting for approval")
	if err := poll(ctx, wait, func() (bool, error) {
		o, err := c.GetOrder(ctx, order.ID)
		if err != nil {
			return false, err
		}
		return o.Status == paypal.OrderApproved, nil
	}); err != nil {
		return err
	}
	ok("approved")

	step("Authorize")
	order, err = c.AuthorizeOrder(ctx, id+"-auth", order.ID)
	if err != nil {
		return err
	}
	return checkAndCapture(ctx, c, order, cents, id)
}

func modeB(ctx context.Context, c *paypal.Client, cents int64, vaultID string, wait time.Duration) error {
	if vaultID == "" {
		step("Create Vault v3 setup token")
		st, err := c.CreatePayPalSetupToken(ctx, "gate1-st-"+randHex(), "Warrant: purchases your agent makes within your permission", returnURL, cancelURL)
		if err != nil {
			return fmt.Errorf("setup token (Mode B may need vault enabled on your sandbox app): %w", err)
		}
		ok("setup token %s status %s", st.ID, st.Status)
		fmt.Printf("\n  Open this link, log in with your sandbox BUYER account and agree:\n  %s\n\n", paypal.LinkHref(st.Links, "approve"))
		step("Waiting for approval")
		if err := poll(ctx, wait, func() (bool, error) {
			s, err := c.GetSetupToken(ctx, st.ID)
			if err != nil {
				return false, err
			}
			return s.Status == paypal.SetupTokenApproved, nil
		}); err != nil {
			return err
		}
		ok("approved")

		step("Create payment token")
		pt, err := c.CreatePaymentToken(ctx, "gate1-pt-"+randHex(), st.ID)
		if err != nil {
			return err
		}
		vaultID = pt.ID
		if err := os.WriteFile(vaultFile, []byte(vaultID), 0o600); err == nil {
			ok("vault_id %s saved to %s (next run skips approval)", mask(vaultID), vaultFile)
		} else {
			ok("vault_id %s", mask(vaultID))
		}
	}

	id := "gate1-" + randHex()
	step("Create order with vault_id, no buyer redirect (intent AUTHORIZE)")
	order, err := c.CreateOrder(ctx, id, paypal.CreateOrderRequest{
		Intent:        paypal.IntentAuthorize,
		PurchaseUnits: []paypal.PurchaseUnit{{CustomID: id, InvoiceID: id, Description: "Warrant gate 1 (vault)", Amount: paypal.USD(cents)}},
		PaymentSource: &paypal.PaymentSource{PayPal: &paypal.PayPalSource{VaultID: vaultID, StoredCredential: paypal.MerchantInitiated()}},
	})
	if err != nil {
		return err
	}
	ok("order %s status %s", order.ID, order.Status)

	switch {
	case order.FirstAuthorization() != nil:
		ok("authorization returned with the create call")
	case order.Status == paypal.OrderPayerActionRequired:
		return errors.New("PayPal asked for buyer action: Mode B is not available for this app, use Mode A")
	default:
		step("Authorize")
		if order, err = c.AuthorizeOrder(ctx, id+"-auth", order.ID); err != nil {
			return err
		}
	}
	return checkAndCapture(ctx, c, order, cents, id)
}

func checkAndCapture(ctx context.Context, c *paypal.Client, order *paypal.Order, cents int64, id string) error {
	auth := order.FirstAuthorization()
	if auth == nil {
		return fmt.Errorf("order %s has no authorization (status %s)", order.ID, order.Status)
	}
	got, err := paypal.ParseCents(auth.Amount.Value)
	if err != nil {
		return err
	}
	ok("authorization %s status %s amount %s", auth.ID, auth.Status, auth.Amount.Value)
	step("Check authorized amount against quote")
	if got != cents || auth.Amount.CurrencyCode != "USD" {
		_ = c.VoidAuthorization(ctx, auth.ID)
		return fmt.Errorf("amount mismatch: quoted %s, authorized %s %s (voided)", paypal.FormatCents(cents), auth.Amount.Value, auth.Amount.CurrencyCode)
	}
	ok("matches")
	step("Capture authorization")
	cp, err := c.CaptureAuthorization(ctx, id+"-cap", auth.ID)
	if err != nil {
		return err
	}
	ok("capture %s status %s", cp.ID, cp.Status)
	if cp.Status != paypal.CaptureCompleted && cp.Status != "PENDING" {
		return fmt.Errorf("capture status %s", cp.Status)
	}
	return nil
}

func poll(ctx context.Context, wait time.Duration, done func() (bool, error)) error {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		ok, err := done()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return errors.New("timed out waiting for buyer approval")
}

func randHex() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func mask(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return "****" + s[len(s)-4:]
}

func step(f string, a ...any) { fmt.Printf("→ "+f+"\n", a...) }
func ok(f string, a ...any)   { fmt.Printf("  ✓ "+f+"\n", a...) }

func fail(err error) {
	fmt.Fprintf(os.Stderr, "\nGATE 1 FAIL: %v\n", err)
	os.Exit(1)
}
