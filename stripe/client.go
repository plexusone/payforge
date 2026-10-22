package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	stripesdk "github.com/stripe/stripe-go/v87"

	"github.com/plexusone/payforge"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// Config configures a Client. It holds a secret: do not log it.
type Config struct {
	// SecretKey is the Stripe secret key. It is required.
	SecretKey string
	// HTTPClient sends the requests. The default is the library's client.
	HTTPClient *http.Client
	// APIURL replaces the Stripe API's base URL. It exists for tests that stand
	// in for Stripe; leave it empty otherwise.
	APIURL string
	// MaxNetworkRetries is how often the library retries a request that failed
	// for a transient reason. The library's retries reuse the idempotency key.
	// Zero uses the library's default of 2.
	MaxNetworkRetries int64
}

// Client implements payforge.Billing for one spec on one Stripe account.
type Client struct {
	spec *pricingspec.Spec
	sc   *stripesdk.Client
}

var _ payforge.Billing = (*Client)(nil)

// New returns a Client. It fails with payforge.ErrNotConfigured when there is no
// secret key, so a host that reaches this point without one is in free mode and
// should use payforge.Disabled, and with the spec's errors when the spec is
// invalid.
func New(spec *pricingspec.Spec, cfg Config) (*Client, error) {
	if cfg.SecretKey == "" {
		return nil, payforge.ErrNotConfigured
	}
	if spec == nil {
		return nil, errors.New("stripe: no pricing spec")
	}
	for _, is := range spec.Validate() {
		return nil, fmt.Errorf("stripe: invalid pricing spec: %s", is.Message)
	}
	bc := &stripesdk.BackendConfig{
		HTTPClient: cfg.HTTPClient,
		// The library's default logger writes request details; a billing
		// library should not.
		LeveledLogger: &stripesdk.LeveledLogger{Level: stripesdk.LevelNull},
	}
	if cfg.APIURL != "" {
		bc.URL = stripesdk.String(strings.TrimRight(cfg.APIURL, "/"))
	}
	if cfg.MaxNetworkRetries > 0 {
		bc.MaxNetworkRetries = stripesdk.Int64(cfg.MaxNetworkRetries)
	}
	return &Client{spec: spec, sc: stripesdk.NewClient(cfg.SecretKey, stripesdk.WithBackends(stripesdk.NewBackendsWithConfig(bc)))}, nil
}

// Enabled reports true: a Client has a key.
func (*Client) Enabled() bool { return true }

// Plan resolves a stored plan; an unknown plan is the spec's default plan.
func (c *Client) Plan(stored string) pricingspec.Plan { return c.spec.PlanOrDefault(stored) }

// subjectPattern limits what may be used as an ID: the ID is placed in a search
// query and in idempotency keys.
var subjectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func checkSubject(s payforge.Subject) error {
	if !subjectPattern.MatchString(s.ID) {
		return fmt.Errorf("%w: ID must be 1 to 128 letters, digits and . _ : -", payforge.ErrInvalidSubject)
	}
	return nil
}

// checkURL requires an absolute http or https address.
func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("%w: %q", payforge.ErrInvalidURL, raw)
	}
	return nil
}

// withSessionID appends the placeholder Stripe replaces with the checkout
// session's ID, unless the URL already has it.
func withSessionID(u string) string {
	if strings.Contains(u, "{CHECKOUT_SESSION_ID}") {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "session_id={CHECKOUT_SESSION_ID}"
}

// priceID finds the active price for a lookup key.
func (c *Client) priceID(ctx context.Context, lookupKey string) (string, error) {
	list := c.sc.V1Prices.List(ctx, &stripesdk.PriceListParams{
		Active:     stripesdk.Bool(true),
		LookupKeys: []*string{stripesdk.String(lookupKey)},
		ListParams: stripesdk.ListParams{Limit: stripesdk.Int64(1)},
	})
	for p, err := range list.All(ctx) {
		if err != nil {
			return "", fmt.Errorf("stripe: look up price %q: %w", lookupKey, err)
		}
		return p.ID, nil
	}
	return "", fmt.Errorf("%w: %s", payforge.ErrNotSynced, lookupKey)
}
