package checkout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/okemwag/warrant/bridge/paypal"
	"github.com/okemwag/warrant/server/ledger"
)

// Webhook errors.
var (
	ErrWebhookNotConfigured = errors.New("checkout: PAYPAL_WEBHOOK_ID is not set, so webhooks can't be verified")
	ErrWebhookUnverified    = errors.New("checkout: webhook signature did not verify")
)

type webhookEvent struct {
	ID         string          `json:"id"`
	EventType  string          `json:"event_type"`
	Resource   json.RawMessage `json:"resource"`
	ResourceID string          `json:"-"`
}

type captureResource struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	CustomID          string `json:"custom_id"`
	SupplementaryData struct {
		RelatedIDs struct {
			OrderID string `json:"order_id"`
		} `json:"related_ids"`
	} `json:"supplementary_data"`
	Links []paypal.Link `json:"links"`
}

// HandleWebhook verifies a PayPal webhook with PayPal, stores it once, and
// applies it. Unverified events are rejected and never acted on.
func (s *Service) HandleWebhook(ctx context.Context, h paypal.WebhookHeaders, body []byte) error {
	if s.cfg.WebhookID == "" {
		return ErrWebhookNotConfigured
	}
	ok, err := s.pp.VerifyWebhook(ctx, s.cfg.WebhookID, h, body)
	if err != nil {
		return s.logPayPal("verify webhook", err)
	}
	if !ok {
		return ErrWebhookUnverified
	}
	var ev webhookEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" {
		return fmt.Errorf("checkout: webhook body: %w", errors.Join(err, errors.New("missing id")))
	}
	var res struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(ev.Resource, &res)

	tag, err := s.pool.Exec(ctx, `INSERT INTO webhook_events (event_id, type, resource_id, payload, received_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (event_id) DO NOTHING`, ev.ID, ev.EventType, res.ID, body, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // duplicate delivery
	}
	applyErr := s.applyWebhook(ctx, ev, res.ID)
	var errText *string
	if applyErr != nil {
		t := applyErr.Error()
		errText = &t
		s.cfg.Logger.Error("webhook", "event", ev.ID, "type", ev.EventType, "err", applyErr)
	}
	_, err = s.pool.Exec(ctx, `UPDATE webhook_events SET processed_at = $2, error = $3 WHERE event_id = $1`, ev.ID, s.now(), errText)
	return err
}

func (s *Service) applyWebhook(ctx context.Context, ev webhookEvent, resourceID string) error {
	switch ev.EventType {
	case "CHECKOUT.ORDER.APPROVED":
		// The buyer approved on PayPal: authorize, check and capture now.
		_, err := s.Complete(ctx, resourceID)
		if errors.Is(err, ledger.ErrNotFound) {
			return nil // not one of ours
		}
		return err
	case "PAYMENT.CAPTURE.COMPLETED", "PAYMENT.CAPTURE.DENIED", "PAYMENT.CAPTURE.DECLINED", "PAYMENT.CAPTURE.REFUNDED":
		var cr captureResource
		if err := json.Unmarshal(ev.Resource, &cr); err != nil {
			return err
		}
		o, err := s.orderForCapture(ctx, cr)
		if errors.Is(err, ledger.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		switch ev.EventType {
		case "PAYMENT.CAPTURE.COMPLETED":
			if o.Status == "refunded" {
				return nil
			}
			if o.CaptureID == nil {
				if _, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET capture_id = $2 WHERE order_id = $1`, o.OrderID, cr.ID); err != nil {
					return err
				}
			}
			return s.markCompleted(ctx, o)
		case "PAYMENT.CAPTURE.REFUNDED":
			// For a refund event the resource is the refund; the order is
			// found through its links, handled by orderForCapture.
			if err := s.l.Refund(ctx, o.ReservationID); err != nil && !errors.Is(err, ledger.ErrConflict) {
				return err
			}
			_, err := s.pool.Exec(ctx, `UPDATE paypal_orders SET status = 'refunded', updated_at = $2 WHERE order_id = $1`, o.OrderID, s.now())
			return err
		default: // denied or declined
			if err := s.l.Release(ctx, o.ReservationID, ledger.ReleasePayFailed); err != nil && !errors.Is(err, ledger.ErrNotHeld) {
				return err
			}
			return s.setStatus(ctx, o, "failed", "PayPal denied the capture", "")
		}
	}
	// Disputes and other events are stored for the evidence pack (Phase 6).
	return nil
}

// orderForCapture finds our order from a capture (or refund) resource.
func (s *Service) orderForCapture(ctx context.Context, cr captureResource) (*Order, error) {
	if id := cr.SupplementaryData.RelatedIDs.OrderID; id != "" {
		if o, err := s.orderByID(ctx, id); err == nil {
			return o, nil
		}
	}
	if cr.CustomID != "" {
		if o, err := s.orderByDecision(ctx, cr.CustomID); err == nil {
			return o, nil
		}
	}
	captureID := cr.ID
	// A refund resource links "up" to its capture: .../captures/{id}.
	if up := paypal.LinkHref(cr.Links, "up"); up != "" {
		if i := strings.LastIndex(up, "/"); i >= 0 {
			captureID = up[i+1:]
		}
	}
	return scanOrder(s.pool.QueryRow(ctx, orderSelect+` WHERE capture_id = $1`, captureID))
}
