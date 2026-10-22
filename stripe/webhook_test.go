package stripe_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87/webhook"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/stripe"
)

// Obviously fake signing secrets.
const (
	whSecret  = "whsec_platform_fake" //nolint:gosec // G101: a fake secret for a test
	whConnect = "whsec_connect_fake"  //nolint:gosec // G101: a fake secret for a test
)

// sign returns a Stripe-Signature header for a payload.
func sign(payload []byte, secret string, at time.Time) string {
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(webhook.ComputeSignature(at, payload, secret)))
}

// event builds an event payload around an object.
func event(id, typ, account, object string) []byte {
	acct := ""
	if account != "" {
		acct = `"account":"` + account + `",`
	}
	return []byte(fmt.Sprintf(`{"id":%q,"object":"event","api_version":"2099-01-01.future","created":1790000000,"livemode":false,%s"type":%q,"data":{"object":%s}}`,
		id, acct, typ, object))
}

func wh() stripe.Webhook { return stripe.Webhook{Secrets: []string{whSecret, whConnect}} }

func parse(t *testing.T, w stripe.Webhook, payload []byte) payforge.Event {
	t.Helper()
	ev, err := w.Parse(payload, sign(payload, whSecret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestWebhookSignature(t *testing.T) {
	payload := event("evt_1", "customer.subscription.created", "", `{"id":"sub_1","status":"active","customer":"cus_1","metadata":{}}`)
	now := time.Now()

	t.Run("either configured secret verifies", func(t *testing.T) {
		for _, secret := range []string{whSecret, whConnect} {
			if _, err := wh().Parse(payload, sign(payload, secret, now)); err != nil {
				t.Errorf("%s: %v", secret, err)
			}
		}
	})
	t.Run("a wrong, tampered, stale or missing signature is refused", func(t *testing.T) {
		tampered := bytes.Replace(payload, []byte("active"), []byte("trialing"), 1)
		for name, tc := range map[string]struct {
			payload []byte
			header  string
		}{
			"wrong secret": {payload, sign(payload, "whsec_other", now)},
			"tampered":     {tampered, sign(payload, whSecret, now)},
			"stale":        {payload, sign(payload, whSecret, now.Add(-time.Hour))},
			"empty":        {payload, ""},
			"malformed":    {payload, "garbage"},
		} {
			_, err := wh().Parse(tc.payload, tc.header)
			if !errors.Is(err, payforge.ErrSignatureInvalid) {
				t.Errorf("%s: err = %v", name, err)
			}
			if err != nil && (strings.Contains(err.Error(), whSecret) || strings.Contains(err.Error(), whConnect)) {
				t.Errorf("%s: a secret is in the error: %v", name, err)
			}
		}
	})
	t.Run("the tolerance can be widened", func(t *testing.T) {
		w := wh()
		w.Tolerance = 2 * time.Hour
		if _, err := w.Parse(payload, sign(payload, whSecret, now.Add(-time.Hour))); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no secret is a configuration error, not a bad signature", func(t *testing.T) {
		for _, w := range []stripe.Webhook{{}, {Secrets: []string{""}}} {
			_, err := w.Parse(payload, sign(payload, whSecret, now))
			if err == nil || errors.Is(err, payforge.ErrSignatureInvalid) {
				t.Errorf("err = %v", err)
			}
		}
	})
}

func TestWebhookRead(t *testing.T) {
	payload := event("evt_1", "customer.subscription.created", "", `{"id":"sub_1","status":"active","metadata":{}}`)
	req := func(body []byte, header string) *http.Request {
		r, _ := http.NewRequest("POST", "/hook", bytes.NewReader(body))
		r.Header.Set("Stripe-Signature", header)
		return r
	}
	ev, err := wh().Read(req(payload, sign(payload, whSecret, time.Now())))
	if err != nil || ev.ID != "evt_1" {
		t.Fatalf("ev = %+v, err = %v", ev, err)
	}
	if _, err := wh().Read(req(payload, "")); !errors.Is(err, payforge.ErrSignatureInvalid) {
		t.Errorf("no header: %v", err)
	}
	small := wh()
	small.MaxBytes = 10
	if _, err := small.Read(req(payload, sign(payload, whSecret, time.Now()))); !errors.Is(err, payforge.ErrPayloadTooLarge) {
		t.Errorf("over the limit: %v", err)
	}
	exact := wh()
	exact.MaxBytes = int64(len(payload))
	if _, err := exact.Read(req(payload, sign(payload, whSecret, time.Now()))); err != nil {
		t.Errorf("exactly the limit: %v", err)
	}
}

func TestWebhookSubscriptionEvents(t *testing.T) {
	obj := `{"id":"sub_1","object":"subscription","status":"past_due","customer":"cus_1","cancel_at_period_end":true,
	  "current_period_end":1795000000,"trial_end":1791000000,
	  "metadata":{"payforge_app":"taskboard","payforge_plan":"plus","payforge_subject":"org-1"},
	  "items":{"data":[{"price":{"id":"price_a","lookup_key":"taskboard/plus/month"}},{"price":{"id":"price_b","lookup_key":"other"}}]}}`
	for typ, kind := range map[string]payforge.EventKind{
		"customer.subscription.created": payforge.EventSubscriptionCreated,
		"customer.subscription.updated": payforge.EventSubscriptionUpdated,
		"customer.subscription.deleted": payforge.EventSubscriptionEnded,
	} {
		ev := parse(t, wh(), event("evt_s", typ, "", obj))
		if ev.Kind != kind || ev.ID != "evt_s" || ev.Type != typ {
			t.Errorf("%s: %+v", typ, ev)
		}
		if ev.App != "taskboard" || ev.Plan != "plus" || ev.Subject != "org-1" || ev.CustomerID != "cus_1" {
			t.Errorf("%s: identity = %+v", typ, ev)
		}
		s := ev.Subscription
		if s == nil || s.ID != "sub_1" || s.Status != "past_due" || !s.CancelAtPeriodEnd {
			t.Fatalf("%s: subscription = %+v", typ, s)
		}
		if s.PeriodEnd.Unix() != 1795000000 || s.TrialEnd.Unix() != 1791000000 {
			t.Errorf("%s: times = %v %v", typ, s.PeriodEnd, s.TrialEnd)
		}
		if len(s.Items) != 2 || s.Items[1].PriceID != "price_b" {
			t.Errorf("%s: every line item should be kept: %+v", typ, s.Items)
		}
		if ev.Created.Unix() != 1790000000 {
			t.Errorf("created = %v", ev.Created)
		}
	}
}

func TestWebhookSubscriptionFallbacks(t *testing.T) {
	t.Run("the plan and app come from the lookup key when the metadata is missing", func(t *testing.T) {
		ev := parse(t, wh(), event("evt_1", "customer.subscription.updated", "", `{"id":"sub_1","status":"active","customer":"cus_1","metadata":{},
		  "items":{"data":[{"price":{"id":"price_a","lookup_key":"taskboard/business/year"}}]}}`))
		if ev.App != "taskboard" || ev.Plan != "business" || ev.Subject != "" {
			t.Fatalf("ev = %+v", ev)
		}
	})
	t.Run("the period end is read from the items on a newer API version", func(t *testing.T) {
		ev := parse(t, wh(), event("evt_1", "customer.subscription.updated", "", `{"id":"sub_1","status":"active","metadata":{},
		  "items":{"data":[{"price":{"id":"a"},"current_period_end":1795000000},{"price":{"id":"b"},"current_period_end":1796000000}]}}`))
		if ev.Subscription.PeriodEnd.Unix() != 1796000000 {
			t.Fatalf("period end = %v", ev.Subscription.PeriodEnd)
		}
	})
	t.Run("a period end that is not given is zero, never now", func(t *testing.T) {
		ev := parse(t, wh(), event("evt_1", "customer.subscription.updated", "", `{"id":"sub_1","status":"active","metadata":{}}`))
		if !ev.Subscription.PeriodEnd.IsZero() || !ev.Subscription.TrialEnd.IsZero() {
			t.Fatalf("times = %v %v", ev.Subscription.PeriodEnd, ev.Subscription.TrialEnd)
		}
	})
	t.Run("an expanded customer is read as its ID", func(t *testing.T) {
		ev := parse(t, wh(), event("evt_1", "customer.subscription.updated", "", `{"id":"sub_1","status":"active","metadata":{},"customer":{"id":"cus_9","object":"customer"}}`))
		if ev.CustomerID != "cus_9" {
			t.Fatalf("customer = %q", ev.CustomerID)
		}
	})
}

func TestWebhookLegacyMetadata(t *testing.T) {
	obj := `{"id":"sub_1","status":"active","metadata":{"myapp_organization_id":"org-7","myapp_plan":"starter","myapp_kind":"plan"}}`
	t.Run("legacy keys are aliases when configured", func(t *testing.T) {
		w := wh()
		w.LegacyPrefixes = []string{"myapp"}
		ev := parse(t, w, event("evt_1", "customer.subscription.updated", "", obj))
		if ev.Subject != "org-7" || ev.Plan != "starter" {
			t.Fatalf("ev = %+v", ev)
		}
		if ev.Metadata["myapp_kind"] != "plan" {
			t.Fatalf("the host's own keys should stay readable: %v", ev.Metadata)
		}
	})
	t.Run("they are not read otherwise", func(t *testing.T) {
		ev := parse(t, wh(), event("evt_1", "customer.subscription.updated", "", obj))
		if ev.Subject != "" || ev.Plan != "" {
			t.Fatalf("ev = %+v", ev)
		}
	})
	t.Run("payforge's own keys win", func(t *testing.T) {
		w := wh()
		w.LegacyPrefixes = []string{"myapp"}
		ev := parse(t, w, event("evt_1", "customer.subscription.updated", "",
			`{"id":"sub_1","status":"active","metadata":{"payforge_subject":"new","myapp_organization_id":"old"}}`))
		if ev.Subject != "new" {
			t.Fatalf("subject = %q", ev.Subject)
		}
	})
}

func TestWebhookCheckout(t *testing.T) {
	for status, paid := range map[string]bool{"paid": true, "no_payment_required": true, "unpaid": false} {
		ev := parse(t, wh(), event("evt_c", "checkout.session.completed", "", fmt.Sprintf(`{"id":"cs_1","mode":"payment","payment_status":%q,
		  "customer":"cus_1","payment_intent":"pi_1","client_reference_id":"purchase-9","amount_total":499,"currency":"usd",
		  "metadata":{"payforge_app":"craftmarket","payforge_product":"sample_pack","payforge_subject":"org-1"}}`, status)))
		c := ev.Checkout
		if ev.Kind != payforge.EventCheckoutCompleted || c == nil {
			t.Fatalf("%s: %+v", status, ev)
		}
		if c.Paid != paid || c.SessionID != "cs_1" || c.Mode != "payment" || c.PaymentIntent != "pi_1" || c.AmountCents != 499 || c.Reference != "purchase-9" {
			t.Errorf("%s: checkout = %+v", status, c)
		}
		if ev.Product != "sample_pack" || ev.App != "craftmarket" {
			t.Errorf("%s: ev = %+v", status, ev)
		}
	}
	ev := parse(t, wh(), event("evt_c", "checkout.session.async_payment_succeeded", "", `{"id":"cs_2","mode":"payment","payment_status":"paid","metadata":{}}`))
	if ev.Kind != payforge.EventCheckoutCompleted || !ev.Checkout.Paid {
		t.Fatalf("async: %+v", ev)
	}
	sub := parse(t, wh(), event("evt_c", "checkout.session.completed", "", `{"id":"cs_3","mode":"subscription","subscription":"sub_1","payment_status":"paid","metadata":{}}`))
	if sub.Checkout.SubscriptionID != "sub_1" {
		t.Fatalf("subscription = %q", sub.Checkout.SubscriptionID)
	}
}

func TestWebhookInvoices(t *testing.T) {
	old := `{"id":"in_1","customer":"cus_1","subscription":"sub_1","amount_due":1200,"currency":"usd","metadata":{}}`
	newer := `{"id":"in_2","customer":"cus_1","amount_due":1200,"currency":"usd","metadata":{},
	  "parent":{"subscription_details":{"subscription":"sub_2","metadata":{"payforge_app":"taskboard","payforge_subject":"org-1"}}}}`
	for typ, kind := range map[string]payforge.EventKind{"invoice.paid": payforge.EventInvoicePaid, "invoice.payment_failed": payforge.EventInvoiceFailed} {
		ev := parse(t, wh(), event("evt_i", typ, "", old))
		if ev.Kind != kind || ev.Invoice == nil || ev.Invoice.SubscriptionID != "sub_1" || ev.Invoice.AmountCents != 1200 {
			t.Errorf("%s: %+v", typ, ev)
		}
	}
	ev := parse(t, wh(), event("evt_i", "invoice.paid", "", newer))
	if ev.Invoice.SubscriptionID != "sub_2" || ev.App != "taskboard" || ev.Subject != "org-1" {
		t.Fatalf("newer shape: %+v", ev)
	}
}

func TestWebhookConnectAccountAndRefund(t *testing.T) {
	ev := parse(t, wh(), event("evt_a", "account.updated", "acct_9",
		`{"id":"acct_9","charges_enabled":true,"payouts_enabled":false,"details_submitted":true,"metadata":{"payforge_subject":"org-5"}}`))
	a := ev.AccountState
	if ev.Kind != payforge.EventAccountUpdated || a == nil || a.ID != "acct_9" || !a.ChargesEnabled || a.PayoutsEnabled || !a.DetailsSubmitted {
		t.Fatalf("account = %+v", ev)
	}
	if ev.Account != "acct_9" || ev.Subject != "org-5" {
		t.Fatalf("ev = %+v", ev)
	}
	own := parse(t, wh(), event("evt_a", "account.updated", "", `{"id":"acct_1","metadata":{}}`))
	if own.Account != "acct_1" {
		t.Fatalf("an account event names its account: %q", own.Account)
	}

	for name, tc := range map[string]struct {
		obj  string
		full bool
	}{
		"full":           {`{"id":"ch_1","payment_intent":"pi_1","amount":2500,"amount_refunded":2500,"refunded":true,"metadata":{}}`, true},
		"full by amount": {`{"id":"ch_1","amount":2500,"amount_refunded":2500,"metadata":{}}`, true},
		"partial":        {`{"id":"ch_1","amount":2500,"amount_refunded":500,"refunded":false,"metadata":{}}`, false},
		"zero amount":    {`{"id":"ch_1","amount":0,"amount_refunded":0,"metadata":{}}`, false},
	} {
		r := parse(t, wh(), event("evt_r", "charge.refunded", "acct_9", tc.obj))
		if r.Kind != payforge.EventRefunded || r.Refund == nil || r.Refund.Full != tc.full {
			t.Errorf("%s: %+v", name, r.Refund)
		}
	}
}

func TestWebhookIgnoresWhatItDoesNotUse(t *testing.T) {
	ev := parse(t, wh(), event("evt_x", "customer.created", "", `{"id":"cus_1","metadata":{"payforge_app":"taskboard"}}`))
	if ev.Kind != payforge.EventIgnored || ev.ID != "evt_x" || ev.Type != "customer.created" {
		t.Fatalf("ev = %+v", ev)
	}
	if ev.Subscription != nil || ev.Checkout != nil || ev.App != "" || ev.CustomerID != "" {
		t.Fatalf("an ignored event should carry nothing else: %+v", ev)
	}
}

func TestWebhookRejectsAMalformedObject(t *testing.T) {
	for name, p := range map[string][]byte{
		"no object": []byte(`{"id":"evt_1","object":"event","created":1790000000,"type":"customer.subscription.updated","data":{}}`),
		"bad type":  event("evt_1", "customer.subscription.updated", "", `{"id":"sub_1","status":{"nested":true},"metadata":{}}`),
	} {
		if _, err := wh().Parse(p, sign(p, whSecret, time.Now())); err == nil || errors.Is(err, payforge.ErrSignatureInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
