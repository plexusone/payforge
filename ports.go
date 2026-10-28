package payforge

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNotFound is returned by a Store lookup that finds nothing.
var ErrNotFound = errors.New("payforge: not found")

// Origin says what caused an entitlement change, so a host that sets a plan by
// hand can decide whether to let the change through.
type Origin string

// OriginSubscription is a change that follows the provider's subscriptions.
const OriginSubscription Origin = "subscription"

// Entitlement is what an account should be on, as the provider's subscriptions
// say now.
type Entitlement struct {
	// Subject is the host's account.
	Subject string
	// Plan is the plan the account should be on: the subscribed plan while it is
	// granted, the spec's default plan otherwise.
	Plan string
	// Granted is true when a subscription grants Plan; false means the account
	// is back on the default plan.
	Granted bool
	// SubscribedPlan is the plan of the subscription behind this, even when it
	// no longer grants; empty when the account has none.
	SubscribedPlan string
	// Status is that subscription's status, empty with none.
	Status string
	// ValidUntil is the end of the paid period, zero when the provider did not
	// say. It is information; the status decides whether the plan is granted.
	ValidUntil     time.Time
	SubscriptionID string
	CustomerID     string
	Origin         Origin
	// EventID is the provider event that led to this.
	EventID string
}

// EntitlementSink is the host's side: it updates its own record of an
// account's plan.
//
// Apply may be called more than once for the same event, because an event is
// recorded as seen only after Apply succeeds and the provider redelivers one
// that did not get a success response. Each call carries the account's whole
// current entitlement, not a change, so Apply must set it, and applying the same
// entitlement twice must leave the same state.
type EntitlementSink interface {
	Apply(ctx context.Context, e Entitlement) error
}

// SaleRefund is a refund of a one-time sale, such as a purchase or a
// marketplace sale.
//
// RefundedCents is the total refunded so far, not this refund's share, and it
// only grows. Apply may see events out of order, so a sink keeps the highest
// total it has seen for a charge and treats Full as permanent. A full refund
// ends the buyer's access to what was bought; the host keeps the data.
type SaleRefund struct {
	// Subject is the buyer's account. It is empty only when neither the sale nor
	// the customer names one, and such a refund is skipped.
	Subject string
	// Seller is the connected account that was paid, empty for a sale without one.
	Seller  string
	Product string
	// ChargeID and PaymentIntent identify the sale, to match the checkout the host
	// recorded.
	ChargeID, PaymentIntent string
	AmountCents             int64
	RefundedCents           int64
	// Full is true when everything has been refunded.
	Full bool
	// EventID is the provider event that led to this.
	EventID string
}

// SaleSink is the host's side for refunds. Like EntitlementSink, ApplyRefund may
// be called more than once for the same event and must leave the same state.
type SaleSink interface {
	ApplyRefund(ctx context.Context, r SaleRefund) error
}

// ChargeDispute is a dispute of a charge: a buyer asked their bank to take a
// payment back. It names the charge and payment intent, not the buyer, because a
// dispute carries no metadata of the sale; the host matches it to the purchase
// or subscription it recorded for that charge.
//
// Open disputes withhold the amount; a host usually suspends what was bought
// while one is open, restores it when Won, and treats Lost like a full refund.
// Events can arrive out of order, so a sink keeps a dispute closed once it has
// seen it closed.
type ChargeDispute struct {
	DisputeID     string
	ChargeID      string
	PaymentIntent string
	AmountCents   int64
	Currency      string
	Reason        string
	// Status is the provider's status. Closed is true once the dispute is
	// decided, and Won or Lost says how; a closed dispute that is neither, such
	// as an inquiry that was closed, took no money.
	Status string
	Closed bool
	Won    bool
	Lost   bool
	// EventID is the provider event that led to this.
	EventID string
}

// DisputeSink is the host's side for disputes. Like EntitlementSink,
// ApplyDispute may be called more than once for the same event and must leave
// the same state.
type DisputeSink interface {
	ApplyDispute(ctx context.Context, d ChargeDispute) error
}

// SellerAccount is a seller's connected account as the provider says now. A
// seller can sell only while ChargesEnabled, and be paid out only while
// PayoutsEnabled; either can turn off again after onboarding.
type SellerAccount struct {
	// Subject is the seller's account in the host, from the account's metadata.
	Subject          string
	AccountID        string
	ChargesEnabled   bool
	PayoutsEnabled   bool
	DetailsSubmitted bool
	EventID          string
}

// SellerSink is the host's side for connected accounts. ApplySeller receives the
// whole current state, so applying it twice leaves the same state.
type SellerSink interface {
	ApplySeller(ctx context.Context, a SellerAccount) error
}

// AccountReader reads a connected account from the provider.
type AccountReader interface {
	Account(ctx context.Context, accountID string) (AccountState, error)
}

// Store is the host's persistence for the lifecycle.
type Store interface {
	// SubjectForCustomer finds the host's account for a provider customer, for an
	// event that does not name one. It returns ErrNotFound when there is none.
	SubjectForCustomer(ctx context.Context, customerID string) (string, error)
	// Seen reports whether an event was already processed.
	Seen(ctx context.Context, eventID string) (bool, error)
	// MarkSeen records that an event was processed.
	MarkSeen(ctx context.Context, eventID string) error
}

// SubscriptionReader reads subscriptions from the provider. With one, the
// lifecycle decides from the provider's current state instead of the event's,
// which makes the order events arrive in irrelevant.
type SubscriptionReader interface {
	// Subscriptions returns the customer's subscriptions that have not ended.
	Subscriptions(ctx context.Context, customerID string) ([]SubscriptionState, error)
}

// Locker serializes work on one account across all of the host's processes,
// for example with a database advisory lock. Lock blocks until the lock for key
// is held or ctx is done, and returns the function that releases it. unlock is
// called exactly once; if releasing fails, the host's implementation logs it,
// and must make sure the lock cannot be held forever (a lock tied to a database
// session or transaction is released when that ends).
//
// The lifecycle holds it around a subscription, refund or account event, and
// the Stripe adapter around a plan checkout, both under LockKey of the app and
// the account, so the two also exclude each other.
type Locker interface {
	Lock(ctx context.Context, key string) (unlock func(), err error)
}

// LockKey is the key a Locker is asked for: one per app and account.
func LockKey(app, subject string) string { return "payforge/" + app + "/" + subject }

// MemoryLocker is a Locker for one process, for tests and for a host that runs
// a single instance.
type MemoryLocker struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// Lock implements Locker.
func (m *MemoryLocker) Lock(ctx context.Context, key string) (func(), error) {
	for {
		m.mu.Lock()
		if m.locks == nil {
			m.locks = map[string]chan struct{}{}
		}
		held, busy := m.locks[key]
		if !busy {
			done := make(chan struct{})
			m.locks[key] = done
			m.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					m.mu.Lock()
					delete(m.locks, key)
					m.mu.Unlock()
					close(done)
				})
			}, nil
		}
		m.mu.Unlock()
		select {
		case <-held:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// MemoryStore is a Store that keeps everything in memory. It is for tests and
// for a host that does not need persistence across restarts.
type MemoryStore struct {
	mu        sync.Mutex
	seen      map[string]bool
	customers map[string]string
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{seen: map[string]bool{}, customers: map[string]string{}}
}

// Link records the host's account for a provider customer.
func (m *MemoryStore) Link(customerID, subject string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.customers[customerID] = subject
}

// SubjectForCustomer implements Store.
func (m *MemoryStore) SubjectForCustomer(_ context.Context, customerID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.customers[customerID]; ok {
		return s, nil
	}
	return "", ErrNotFound
}

// Seen implements Store.
func (m *MemoryStore) Seen(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[eventID], nil
}

// MarkSeen implements Store.
func (m *MemoryStore) MarkSeen(_ context.Context, eventID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen[eventID] = true
	return nil
}

// MemorySink is an EntitlementSink, SaleSink, SellerSink and DisputeSink that
// keeps the latest entitlement for each account in memory, with the history of
// what it was given.
type MemorySink struct {
	mu       sync.Mutex
	current  map[string]Entitlement
	history  []Entitlement
	refunds  map[string]SaleRefund
	sellers  map[string]SellerAccount
	disputes map[string]ChargeDispute
}

// NewMemorySink returns an empty MemorySink.
func NewMemorySink() *MemorySink { return &MemorySink{current: map[string]Entitlement{}} }

// Apply implements EntitlementSink.
func (m *MemorySink) Apply(_ context.Context, e Entitlement) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current[e.Subject] = e
	m.history = append(m.history, e)
	return nil
}

// Get returns the latest entitlement applied for an account.
func (m *MemorySink) Get(subject string) (Entitlement, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.current[subject]
	return e, ok
}

// History returns every entitlement applied, oldest first.
func (m *MemorySink) History() []Entitlement {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Entitlement(nil), m.history...)
}

// ApplyRefund implements SaleSink. It keeps the highest refunded total seen for
// a charge, and a full refund stays full.
func (m *MemorySink) ApplyRefund(_ context.Context, r SaleRefund) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refunds == nil {
		m.refunds = map[string]SaleRefund{}
	}
	if old, ok := m.refunds[r.ChargeID]; ok {
		if old.RefundedCents > r.RefundedCents {
			r.RefundedCents = old.RefundedCents
		}
		r.Full = r.Full || old.Full
	}
	m.refunds[r.ChargeID] = r
	return nil
}

// Refund returns the state kept for a charge.
func (m *MemorySink) Refund(chargeID string) (SaleRefund, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.refunds[chargeID]
	return r, ok
}

// ApplySeller implements SellerSink.
func (m *MemorySink) ApplySeller(_ context.Context, a SellerAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sellers == nil {
		m.sellers = map[string]SellerAccount{}
	}
	m.sellers[a.AccountID] = a
	return nil
}

// Seller returns the latest state applied for a connected account.
func (m *MemorySink) Seller(accountID string) (SellerAccount, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.sellers[accountID]
	return a, ok
}

// ApplyDispute implements DisputeSink. A closed dispute stays closed.
func (m *MemorySink) ApplyDispute(_ context.Context, d ChargeDispute) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disputes == nil {
		m.disputes = map[string]ChargeDispute{}
	}
	if old, ok := m.disputes[d.DisputeID]; ok && old.Closed && !d.Closed {
		return nil
	}
	m.disputes[d.DisputeID] = d
	return nil
}

// Dispute returns the latest state applied for a dispute.
func (m *MemorySink) Dispute(disputeID string) (ChargeDispute, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.disputes[disputeID]
	return d, ok
}
