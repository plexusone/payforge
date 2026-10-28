package webhook_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	stripesdk "github.com/stripe/stripe-go/v87/webhook"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/examples/webhook"
	"github.com/plexusone/payforge/stripe"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

const secret = "whsec_example_fake" //nolint:gosec // G101: a fake secret for a test

func sign(payload []byte, at time.Time) string {
	return fmt.Sprintf("t=%d,v1=%s", at.Unix(), hex.EncodeToString(stripesdk.ComputeSignature(at, payload, secret)))
}

const subscription = `{"id":"evt_1","object":"event","created":1790000000,"livemode":false,
"type":"customer.subscription.created","data":{"object":{"id":"sub_1","status":"active","customer":"cus_1",
"metadata":{"payforge_app":"taskboard","payforge_subject":"org-1"},
"items":{"data":[{"price":{"id":"price_a","lookup_key":"taskboard/plus/month"}}]}}}}`

type rig struct {
	srv  http.Handler
	sink *payforge.MemorySink
	logs *bytes.Buffer
}

func newRig(t *testing.T, secrets ...string) *rig {
	t.Helper()
	f, err := os.Open("../catalog/pricing.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	spec, err := pricingspec.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{sink: payforge.NewMemorySink(), logs: &bytes.Buffer{}}
	l := &payforge.Lifecycle{Spec: spec, Store: payforge.NewMemoryStore(), Sink: r.sink}
	r.srv = webhook.Handler(stripe.Webhook{Mode: stripe.ModeTest, Secrets: secrets}, l, slog.New(slog.NewTextHandler(r.logs, nil)))
	return r
}

func (r *rig) post(body, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(body))
	req.Header.Set("Stripe-Signature", header)
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, req)
	return w
}

func TestASignedEventChangesThePlan(t *testing.T) {
	r := newRig(t, secret)
	if w := r.post(subscription, sign([]byte(subscription), time.Now())); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	got, ok := r.sink.Get("org-1")
	if !ok || got.Plan != "plus" || !got.Granted {
		t.Fatalf("held %+v", got)
	}
	// Stripe redelivers: the same answer, the same state.
	if w := r.post(subscription, sign([]byte(subscription), time.Now())); w.Code != http.StatusOK {
		t.Fatalf("redelivery status = %d", w.Code)
	}
	if n := len(r.sink.History()); n != 1 {
		t.Fatalf("applied %d times", n)
	}
}

func TestWhatIsNotFromStripeIsRefusedAndChangesNothing(t *testing.T) {
	r := newRig(t, secret)
	tampered := strings.Replace(subscription, "plus", "business", 1)
	for name, header := range map[string]string{
		"no signature": "", "tampered": sign([]byte(subscription), time.Now()),
		"stale": sign([]byte(tampered), time.Now().Add(-time.Hour)),
	} {
		if w := r.post(tampered, header); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d", name, w.Code)
		}
	}
	if _, ok := r.sink.Get("org-1"); ok {
		t.Fatal("an unverified event changed a plan")
	}
}

func TestAConfigurationProblemIsOursSoItIsA5xx(t *testing.T) {
	r := newRig(t) // no secret
	if w := r.post(subscription, sign([]byte(subscription), time.Now())); w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(r.logs.String(), "cannot read event") {
		t.Errorf("logs = %q", r.logs.String())
	}
}

func TestOnlyPostIsAccepted(t *testing.T) {
	r := newRig(t, secret)
	w := httptest.NewRecorder()
	r.srv.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/hook", http.NoBody))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", w.Code)
	}
}
