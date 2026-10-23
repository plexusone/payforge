// Package webhook shows a Stripe webhook endpoint: verify the request, hand the
// event to the lifecycle, and answer so Stripe retries only what a retry can fix.
package webhook

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/plexusone/payforge"
	"github.com/plexusone/payforge/stripe"
)

// Handler returns the endpoint. The lifecycle's Store and Sink are yours; set
// its Provider to the Stripe client in production so event order cannot matter,
// and hold a lock per account around Handle if events can be handled at once.
//
// Serve it from an http.Server with ReadHeaderTimeout and ReadTimeout set: the
// body is read before the signature is checked, so a slow sender otherwise holds
// a connection open.
func Handler(wh stripe.Webhook, l *payforge.Lifecycle, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ev, err := wh.Read(r)
		switch {
		case errors.Is(err, payforge.ErrSignatureInvalid):
			// Not from Stripe, or stale. A retry cannot fix it.
			http.Error(w, "bad signature", http.StatusBadRequest)
			return
		case errors.Is(err, payforge.ErrPayloadTooLarge):
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		case err != nil:
			// Signed by Stripe but unreadable, or no secret configured: our
			// problem, so answer 5xx and let it be noticed and redelivered.
			log.Error("webhook: cannot read event", "error", err)
			http.Error(w, "cannot read event", http.StatusInternalServerError)
			return
		}
		res, err := l.Handle(r.Context(), ev)
		if err != nil {
			// The store, the provider or the sink failed: Stripe redelivers.
			log.Error("webhook: cannot apply event", "event", ev.ID, "type", ev.Type, "error", err)
			http.Error(w, "cannot apply event", http.StatusInternalServerError)
			return
		}
		if res.Outcome == payforge.OutcomeSkipped {
			log.Warn("webhook: event skipped", "event", ev.ID, "type", ev.Type, "reason", res.Reason)
		}
		w.WriteHeader(http.StatusOK)
	})
}
