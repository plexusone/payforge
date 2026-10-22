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

// MemorySink is an EntitlementSink that keeps the latest entitlement for each
// account in memory, with the history of what it was given.
type MemorySink struct {
	mu      sync.Mutex
	current map[string]Entitlement
	history []Entitlement
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
