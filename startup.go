package payforge

import (
	"errors"
	"fmt"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

// ProviderConfig is the host's provider configuration. It holds secrets: do not
// log it. The zero value means no provider, which is free mode.
type ProviderConfig struct {
	// SecretKey is the provider's secret API key. Empty means billing is not
	// configured.
	SecretKey string
	// WebhookSecrets are the signing secrets of the webhook endpoints. A
	// marketplace needs a second, for the provider's connected-account events.
	WebhookSecrets []string
}

// Configured reports whether a provider key is set.
func (c ProviderConfig) Configured() bool { return c.SecretKey != "" }

// CheckStartup returns an error when billing is half-configured or the spec
// cannot be billed, so a mistake stops the host at startup instead of showing as
// plans that never change. With no key it succeeds: that is free mode.
//
// With a key it requires the spec to be valid, a webhook secret (two when the
// spec has a marketplace or seller-set products, because connected-account
// events arrive on their own endpoint), and a plan to sell. Errors name what is
// missing and never contain a secret.
func CheckStartup(spec *pricingspec.Spec, cfg ProviderConfig) error {
	if spec == nil {
		return errors.New("payforge: no pricing spec")
	}
	if !cfg.Configured() {
		if len(cfg.WebhookSecrets) > 0 {
			return errors.New("payforge: a webhook secret is set but no provider key: set both or neither")
		}
		return nil
	}

	var errs []error
	if issues := spec.Validate(); len(issues) > 0 {
		errs = append(errs, &pricingspec.ValidationError{Issues: issues})
	}
	need := 1
	if needsConnect(spec) {
		need = 2
	}
	have := 0
	for _, s := range cfg.WebhookSecrets {
		if s != "" {
			have++
		}
	}
	if have < need {
		errs = append(errs, fmt.Errorf("payforge: %d webhook secret(s) set, %d required%s",
			have, need, connectNote(need)))
	}
	if !hasPaidPlan(spec) && len(spec.Products) == 0 && spec.Marketplace == nil {
		errs = append(errs, errors.New("payforge: a provider is configured but the spec has nothing to sell"))
	}
	return errors.Join(errs...)
}

func needsConnect(spec *pricingspec.Spec) bool {
	if spec.Marketplace != nil {
		return true
	}
	for _, p := range spec.Products {
		if p.SellerSet {
			return true
		}
	}
	return false
}

func connectNote(need int) string {
	if need > 1 {
		return " (the spec has a marketplace, so connected-account events need their own endpoint and secret)"
	}
	return ""
}

func hasPaidPlan(spec *pricingspec.Spec) bool {
	for _, p := range spec.Plans {
		if len(p.Prices) > 0 && p.SelfServe {
			return true
		}
	}
	return false
}
