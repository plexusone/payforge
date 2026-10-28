package payforge

import (
	"errors"
	"time"
)

// Errors from reading a webhook.
var (
	// ErrSignatureInvalid means no configured secret verifies the payload's
	// signature, or the signature is missing, malformed or too old. The caller
	// should answer 400 and not retry.
	ErrSignatureInvalid = errors.New("payforge: webhook signature invalid")
	// ErrPayloadTooLarge means the request body is over the size limit.
	ErrPayloadTooLarge = errors.New("payforge: webhook payload too large")
)

// EventKind says what an Event is about.
type EventKind string

const (
	// EventCheckoutCompleted is a checkout session that finished. Paid says
	// whether the money arrived, which a delayed payment method confirms later.
	EventCheckoutCompleted EventKind = "checkout_completed"
	// EventCheckoutFailed is a checkout whose delayed payment, such as a bank
	// debit, failed after the session completed. Checkout carries it, with Paid
	// false. A host that granted a one-time purchase on completion revokes it.
	EventCheckoutFailed EventKind = "checkout_failed"
	// EventSubscriptionCreated, EventSubscriptionUpdated and EventSubscriptionEnded
	// carry the subscription's state. An ended subscription never grants.
	EventSubscriptionCreated EventKind = "subscription_created"
	EventSubscriptionUpdated EventKind = "subscription_updated"
	EventSubscriptionEnded   EventKind = "subscription_ended"
	// EventInvoicePaid and EventInvoiceFailed report a renewal's payment.
	EventInvoicePaid   EventKind = "invoice_paid"
	EventInvoiceFailed EventKind = "invoice_failed"
	// EventAccountUpdated is a connected account's state changing.
	EventAccountUpdated EventKind = "account_updated"
	// EventRefunded is a charge refunded, in full or in part.
	EventRefunded EventKind = "refunded"
	// EventDisputeOpened is a buyer disputing a charge with their bank (a
	// chargeback); EventDisputeClosed is the dispute decided. Dispute carries it.
	EventDisputeOpened EventKind = "dispute_opened"
	EventDisputeClosed EventKind = "dispute_closed"
	// EventIgnored is any other event. It has an ID and a Type and nothing else,
	// so a host can acknowledge it and move on.
	EventIgnored EventKind = "ignored"
)

// Event is a webhook reduced to the few fields the lifecycle needs. Only one of
// the detail pointers is set, matching Kind.
type Event struct {
	// ID is the provider's event ID, the key for processing an event once.
	ID string
	// Type is the provider's own event type, kept for logs.
	Type string
	Kind EventKind
	// Created is when the provider created the event. It is not the order the
	// provider will deliver events in.
	Created  time.Time
	Livemode bool
	// Account is the connected account the event happened on, empty for the
	// platform's own account.
	Account string

	// App, Subject, Plan and Product come from the object's metadata, or for
	// App and Plan from a price's lookup key when the metadata is missing. Empty
	// when the object carries none; an event for an object with a different App
	// belongs to another product sharing the account.
	App, Subject, Plan, Product string
	CustomerID                  string
	// Metadata is a copy of the object's metadata, so a host can read keys of
	// its own.
	Metadata map[string]string

	Checkout     *CheckoutResult
	Subscription *SubscriptionState
	Invoice      *InvoiceResult
	AccountState *AccountState
	Refund       *RefundResult
	Dispute      *DisputeResult
}

// CheckoutResult is a finished checkout session.
type CheckoutResult struct {
	SessionID string
	// Mode is "subscription", "payment" or "setup".
	Mode string
	// Paid is true when the money has arrived; a checkout with no payment due,
	// such as a trial, also counts.
	Paid           bool
	SubscriptionID string
	PaymentIntent  string
	AmountCents    int64
	Currency       string
	// Reference is the client reference ID the session was created with.
	Reference string
}

// SubscriptionState is a subscription as of an event.
type SubscriptionState struct {
	ID     string
	Status string
	// Items are every line of the subscription, not only the first.
	Items []SubscriptionItem
	// PeriodEnd is the end of the paid period; zero when the event did not say.
	PeriodEnd         time.Time
	CancelAtPeriodEnd bool
	TrialEnd          time.Time
	// Metadata is a copy of the subscription's metadata, which names the plan
	// when its price carries no lookup key.
	Metadata map[string]string
}

// SubscriptionItem is one line of a subscription.
type SubscriptionItem struct {
	PriceID string
	// LookupKey is the price's lookup key, which names the plan and interval
	// for a price payforge synced.
	LookupKey string
}

// InvoiceResult is a renewal's invoice.
type InvoiceResult struct {
	ID             string
	SubscriptionID string
	AmountCents    int64
	Currency       string
}

// AccountState is a connected account's capabilities.
type AccountState struct {
	ID               string
	ChargesEnabled   bool
	PayoutsEnabled   bool
	DetailsSubmitted bool
}

// RefundResult is a refunded charge.
type RefundResult struct {
	ChargeID      string
	PaymentIntent string
	// AmountCents is the charge's amount and RefundedCents the total refunded so
	// far. Full is true only when everything has been refunded.
	AmountCents   int64
	RefundedCents int64
	Full          bool
}

// DisputeResult is a disputed charge.
type DisputeResult struct {
	ID            string
	ChargeID      string
	PaymentIntent string
	// AmountCents is the amount disputed, which the provider holds back from the
	// account while the dispute is open.
	AmountCents int64
	Currency    string
	// Reason is the provider's reason code, such as "fraudulent".
	Reason string
	// Status is the provider's status, such as "needs_response", "won" or "lost".
	Status string
}

// MetadataSubject reads the host's subject from an object's metadata: payforge's
// own key first, then each legacy prefix's "<prefix>_organization_id".
func MetadataSubject(meta map[string]string, legacyPrefixes []string) string {
	if v := meta[MetaSubject]; v != "" {
		return v
	}
	for _, p := range legacyPrefixes {
		if v := meta[p+"_organization_id"]; v != "" {
			return v
		}
	}
	return ""
}

// MetadataPlan reads the plan from an object's metadata: payforge's own key
// first, then each legacy prefix's "<prefix>_plan".
func MetadataPlan(meta map[string]string, legacyPrefixes []string) string {
	if v := meta[MetaPlan]; v != "" {
		return v
	}
	for _, p := range legacyPrefixes {
		if v := meta[p+"_plan"]; v != "" {
			return v
		}
	}
	return ""
}
