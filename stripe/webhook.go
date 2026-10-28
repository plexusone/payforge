package stripe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	stripesdk "github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/plexusone/payforge"
)

const (
	defaultTolerance = 5 * time.Minute
	defaultMaxBytes  = 1 << 20
)

// Webhook verifies and reads Stripe webhook requests. It holds secrets: do not
// log it.
type Webhook struct {
	// Mode is the mode of the API key the host uses, usually Client.Mode() or
	// KeyMode(key). It is required. An event whose livemode does not match is
	// returned as payforge.EventIgnored, to be acknowledged and not acted on, so
	// a test-mode secret configured by mistake cannot change live accounts.
	Mode Mode
	// Secrets are the signing secrets of the endpoints that deliver here. A
	// payload is accepted if any one verifies it, so the platform's endpoint and
	// the connected-account endpoint can share a handler.
	Secrets []string
	// LegacyPrefixes name host-specific metadata prefixes written before
	// payforge, such as "myapp" for myapp_organization_id and myapp_plan. They
	// are read as aliases for the subject and plan, so objects that already exist
	// at Stripe keep working.
	LegacyPrefixes []string
	// Tolerance is how old a signature may be. Zero is five minutes.
	Tolerance time.Duration
	// MaxBytes limits the body Read accepts. Zero is 1 MiB.
	MaxBytes int64
}

// Read verifies the request and reduces it to an Event. It reads at most
// MaxBytes of the body and answers payforge.ErrPayloadTooLarge beyond that.
func (w Webhook) Read(r *http.Request) (payforge.Event, error) {
	limit := w.MaxBytes
	if limit <= 0 {
		limit = defaultMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return payforge.Event{}, fmt.Errorf("stripe: read webhook body: %w", err)
	}
	if int64(len(body)) > limit {
		return payforge.Event{}, payforge.ErrPayloadTooLarge
	}
	return w.Parse(body, r.Header.Get("Stripe-Signature"))
}

// Parse verifies a payload against its Stripe-Signature header and reduces it
// to an Event.
//
// An event of the other mode than Mode comes back as EventIgnored with only its
// ID, type, creation time, livemode and account, never acted on.
//
// The signature is checked before anything else is read. Only a few stable
// fields are taken from the payload, so an endpoint pinned to a different API
// version than this library's still works. An event type the lifecycle does not
// use comes back as EventIgnored with only its ID and type.
func (w Webhook) Parse(payload []byte, header string) (payforge.Event, error) {
	if !w.Mode.valid() {
		// A configuration error, like a missing secret: answered with a 5xx, so
		// nothing is lost while it is fixed.
		return payforge.Event{}, errors.New("stripe: webhook Mode is not set: use the client's Mode()")
	}
	ev, err := w.verify(payload, header)
	if err != nil {
		return payforge.Event{}, err
	}
	out := payforge.Event{
		ID: ev.ID, Type: string(ev.Type), Kind: payforge.EventIgnored,
		Created: time.Unix(ev.Created, 0).UTC(), Livemode: ev.Livemode, Account: ev.Account,
	}
	if ev.Livemode != w.Mode.livemode() {
		return out, nil // the other mode's event: not ours to act on
	}
	kind, ok := kinds[string(ev.Type)]
	if !ok {
		return out, nil
	}
	if ev.Data == nil || len(ev.Data.Raw) == 0 {
		return payforge.Event{}, fmt.Errorf("stripe: %s event %s has no object", ev.Type, ev.ID)
	}
	out.Kind = kind
	if err := w.fill(&out, ev.Data.Raw); err != nil {
		return payforge.Event{}, fmt.Errorf("stripe: read %s event %s: %w", ev.Type, ev.ID, err)
	}
	return out, nil
}

func (w Webhook) verify(payload []byte, header string) (stripesdk.Event, error) {
	tol := w.Tolerance
	if tol <= 0 {
		tol = defaultTolerance
	}
	var tried int
	var last error
	verified := false
	for _, secret := range w.Secrets {
		if secret == "" {
			continue
		}
		tried++
		if last = webhook.ValidatePayloadWithTolerance(payload, header, secret, tol); last == nil {
			verified = true
			break
		}
	}
	if tried == 0 {
		return stripesdk.Event{}, errors.New("stripe: no webhook secret configured")
	}
	if !verified {
		// The library's errors name the failure, never the secret.
		return stripesdk.Event{}, fmt.Errorf("%w: %v", payforge.ErrSignatureInvalid, last)
	}
	// The payload is read only after its signature checks out, and into the
	// library's loose event type: the account may be pinned to another API
	// version, and the fields used here are stable across versions.
	var ev stripesdk.Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		return stripesdk.Event{}, fmt.Errorf("stripe: read webhook event: %w", err)
	}
	return ev, nil
}

// kinds maps Stripe's event types to the normalized kinds.
var kinds = map[string]payforge.EventKind{
	"checkout.session.completed":               payforge.EventCheckoutCompleted,
	"checkout.session.async_payment_succeeded": payforge.EventCheckoutCompleted,
	"customer.subscription.created":            payforge.EventSubscriptionCreated,
	"customer.subscription.updated":            payforge.EventSubscriptionUpdated,
	"customer.subscription.deleted":            payforge.EventSubscriptionEnded,
	"invoice.paid":                             payforge.EventInvoicePaid,
	"invoice.payment_failed":                   payforge.EventInvoiceFailed,
	"account.updated":                          payforge.EventAccountUpdated,
	"charge.refunded":                          payforge.EventRefunded,
}

// ref is a field that is an ID, or an expanded object that has one.
type ref string

func (r *ref) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*r = ref(s)
		return nil
	}
	var o struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &o); err != nil {
		return err
	}
	*r = ref(o.ID)
	return nil
}

type rawItem struct {
	Price struct {
		ID        string `json:"id"`
		LookupKey string `json:"lookup_key"`
	} `json:"price"`
	CurrentPeriodEnd int64 `json:"current_period_end"`
}

type rawObject struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
	Customer ref               `json:"customer"`

	// subscription
	Status            string `json:"status"`
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64  `json:"current_period_end"`
	TrialEnd          int64  `json:"trial_end"`
	Items             struct {
		Data []rawItem `json:"data"`
	} `json:"items"`

	// checkout session
	Mode              string `json:"mode"`
	PaymentStatus     string `json:"payment_status"`
	ClientReferenceID string `json:"client_reference_id"`
	AmountTotal       int64  `json:"amount_total"`
	Currency          string `json:"currency"`
	PaymentIntent     ref    `json:"payment_intent"`

	// A checkout session's subscription, and an invoice's: the invoice moved it
	// under parent in newer API versions.
	Subscription ref `json:"subscription"`
	Parent       struct {
		SubscriptionDetails struct {
			Subscription ref               `json:"subscription"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
	SubscriptionDetails struct {
		Metadata map[string]string `json:"metadata"`
	} `json:"subscription_details"`

	// invoice
	AmountDue int64 `json:"amount_due"`

	// account
	ChargesEnabled   bool `json:"charges_enabled"`
	PayoutsEnabled   bool `json:"payouts_enabled"`
	DetailsSubmitted bool `json:"details_submitted"`

	// charge
	Amount         int64 `json:"amount"`
	AmountRefunded int64 `json:"amount_refunded"`
	Refunded       bool  `json:"refunded"`
}

func unix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func (w Webhook) fill(out *payforge.Event, raw json.RawMessage) error {
	var o rawObject
	if err := json.Unmarshal(raw, &o); err != nil {
		return err
	}
	meta := o.Metadata
	if len(meta) == 0 {
		// An invoice carries its subscription's metadata, under a name that
		// depends on the API version.
		if m := o.Parent.SubscriptionDetails.Metadata; len(m) > 0 {
			meta = m
		} else {
			meta = o.SubscriptionDetails.Metadata
		}
	}
	out.CustomerID = string(o.Customer)
	out.Metadata = make(map[string]string, len(meta))
	for k, v := range meta {
		out.Metadata[k] = v
	}
	out.App = meta[payforge.MetaApp]
	out.Subject = payforge.MetadataSubject(meta, w.LegacyPrefixes)
	out.Plan = payforge.MetadataPlan(meta, w.LegacyPrefixes)
	out.Product = meta[payforge.MetaProduct]

	switch out.Kind {
	case payforge.EventCheckoutCompleted:
		out.Checkout = &payforge.CheckoutResult{
			SessionID: o.ID, Mode: o.Mode,
			Paid:           o.PaymentStatus == "paid" || o.PaymentStatus == "no_payment_required",
			SubscriptionID: string(o.Subscription), PaymentIntent: string(o.PaymentIntent),
			AmountCents: o.AmountTotal, Currency: o.Currency, Reference: o.ClientReferenceID,
		}
	case payforge.EventSubscriptionCreated, payforge.EventSubscriptionUpdated, payforge.EventSubscriptionEnded:
		s := &payforge.SubscriptionState{
			ID: o.ID, Status: o.Status, CancelAtPeriodEnd: o.CancelAtPeriodEnd,
			PeriodEnd: unix(o.CurrentPeriodEnd), TrialEnd: unix(o.TrialEnd), Metadata: out.Metadata,
		}
		for _, it := range o.Items.Data {
			s.Items = append(s.Items, payforge.SubscriptionItem{PriceID: it.Price.ID, LookupKey: it.Price.LookupKey})
			// Newer API versions put the period on the items, not the subscription.
			if end := unix(it.CurrentPeriodEnd); o.CurrentPeriodEnd == 0 && end.After(s.PeriodEnd) {
				s.PeriodEnd = end
			}
			if out.App == "" || out.Plan == "" {
				if app, plan, _, ok := payforge.ParsePlanLookupKey(it.Price.LookupKey); ok {
					if out.App == "" {
						out.App = app
					}
					if out.Plan == "" {
						out.Plan = plan
					}
				}
			}
		}
		out.Subscription = s
	case payforge.EventInvoicePaid, payforge.EventInvoiceFailed:
		sub := string(o.Subscription)
		if sub == "" {
			sub = string(o.Parent.SubscriptionDetails.Subscription)
		}
		out.Invoice = &payforge.InvoiceResult{ID: o.ID, SubscriptionID: sub, AmountCents: o.AmountDue, Currency: o.Currency}
	case payforge.EventAccountUpdated:
		out.AccountState = &payforge.AccountState{
			ID: o.ID, ChargesEnabled: o.ChargesEnabled, PayoutsEnabled: o.PayoutsEnabled, DetailsSubmitted: o.DetailsSubmitted,
		}
		if out.Account == "" {
			out.Account = o.ID
		}
	case payforge.EventRefunded:
		out.Refund = &payforge.RefundResult{
			ChargeID: o.ID, PaymentIntent: string(o.PaymentIntent), AmountCents: o.Amount, RefundedCents: o.AmountRefunded,
			Full: o.Refunded || (o.Amount > 0 && o.AmountRefunded >= o.Amount),
		}
	}
	return nil
}
