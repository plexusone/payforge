package payforge_test

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/plexusone/payforge"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func load(t *testing.T, name string) *pricingspec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s, err := pricingspec.Parse(data)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return s
}

const secret = "sk_test_do_not_leak_this_value" //nolint:gosec // G101: an obviously fake key for a test

func TestCheckStartup(t *testing.T) {
	tool := load(t, "subscription-tool.json")
	market := load(t, "marketplace.json")
	tests := []struct {
		name    string
		spec    *pricingspec.Spec
		cfg     payforge.ProviderConfig
		wantErr string
	}{
		{"free mode needs nothing", tool, payforge.ProviderConfig{}, ""},
		{"a key and a webhook secret", tool, payforge.ProviderConfig{SecretKey: secret, WebhookSecrets: []string{"whsec_a"}}, ""},
		{"a key without a webhook secret", tool, payforge.ProviderConfig{SecretKey: secret}, "0 webhook secret(s) set, 1 required"},
		{"a blank webhook secret does not count", tool, payforge.ProviderConfig{SecretKey: secret, WebhookSecrets: []string{""}}, "0 webhook secret(s)"},
		{"a webhook secret without a key", tool, payforge.ProviderConfig{WebhookSecrets: []string{"whsec_a"}}, "set both or neither"},
		{"a marketplace needs a second secret", market, payforge.ProviderConfig{SecretKey: secret, WebhookSecrets: []string{"whsec_a"}}, "connected-account events"},
		{"a marketplace with both secrets", market, payforge.ProviderConfig{SecretKey: secret, WebhookSecrets: []string{"whsec_a", "whsec_b"}}, ""},
		{"no spec", nil, payforge.ProviderConfig{}, "no pricing spec"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := payforge.CheckStartup(tc.spec, tc.cfg)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("error contains the secret key: %v", err)
			}
		})
	}
}

func TestCheckStartupRejectsAnInvalidSpecOnlyWhenBilling(t *testing.T) {
	bad := load(t, "subscription-tool.json")
	bad.DefaultPlan = "nope"
	if err := payforge.CheckStartup(bad, payforge.ProviderConfig{}); err != nil {
		t.Fatalf("free mode should not fail: %v", err)
	}
	err := payforge.CheckStartup(bad, payforge.ProviderConfig{SecretKey: secret, WebhookSecrets: []string{"w"}})
	var ve *pricingspec.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
}

func TestNothingToSell(t *testing.T) {
	free := load(t, "subscription-tool.json")
	free.Plans = free.Plans[:1]
	free.Entitlements = free.Entitlements[:1]
	err := payforge.CheckStartup(free, payforge.ProviderConfig{SecretKey: secret, WebhookSecrets: []string{"w"}})
	if err == nil || !strings.Contains(err.Error(), "nothing to sell") {
		t.Fatalf("err = %v", err)
	}
}

func TestDisabledIsFreeMode(t *testing.T) {
	spec := load(t, "subscription-tool.json")
	b := payforge.Disabled(spec)
	if b.Enabled() {
		t.Fatal("enabled")
	}
	for _, stored := range []string{"", "plus", "business", "enterprise", "nope"} {
		if got := b.Plan(stored).ID; got != "free" {
			t.Errorf("Plan(%q) = %q, want the default plan", stored, got)
		}
	}
	if _, err := b.CheckoutPlan(context.Background(), payforge.PlanCheckout{Plan: "plus"}); !errors.Is(err, payforge.ErrNotConfigured) {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := b.Portal(context.Background(), "cus_1", "https://example.com"); !errors.Is(err, payforge.ErrNotConfigured) {
		t.Fatalf("portal: %v", err)
	}
}

func TestLookupKeys(t *testing.T) {
	key := payforge.PlanLookupKey("taskboard", "plus", pricingspec.IntervalYear)
	if key != "taskboard/plus/year" {
		t.Fatal(key)
	}
	app, plan, iv, ok := payforge.ParsePlanLookupKey(key)
	if !ok || app != "taskboard" || plan != "plus" || iv != pricingspec.IntervalYear {
		t.Fatalf("parsed %q %q %q %t", app, plan, iv, ok)
	}
	for _, foreign := range []string{"", "plus", "a/b", "a/b/week", "/b/month", "a//month", "a/b/month/extra", "sku_123"} {
		if _, _, _, ok := payforge.ParsePlanLookupKey(foreign); ok {
			t.Errorf("accepted %q", foreign)
		}
	}
	if payforge.ProductLookupKey("craftmarket", "sample_pack") != "craftmarket/sample_pack" {
		t.Fatal("product key")
	}
}

func TestProviderPlanGolden(t *testing.T) {
	for _, name := range []string{"subscription-tool", "marketplace"} {
		t.Run(name, func(t *testing.T) {
			plan := payforge.ProviderPlanFor(load(t, name+".json"))
			got, err := json.MarshalIndent(plan, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			path := filepath.Join("testdata", "golden", name+".provider.json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if string(want) != string(got) {
				t.Fatalf("differs from %s; run go test -update and review\n%s", path, got)
			}
		})
	}
}

func TestProviderPlanContents(t *testing.T) {
	plan := payforge.ProviderPlanFor(load(t, "marketplace.json"))
	keys := map[string]bool{}
	for _, p := range plan.Prices {
		keys[p.LookupKey] = true
		if p.Metadata[payforge.MetaApp] != "craftmarket" {
			t.Errorf("price %s is not tagged with the app", p.LookupKey)
		}
	}
	for _, want := range []string{"craftmarket/seller/month", "craftmarket/seller/year", "craftmarket/pro_seller/month", "craftmarket/sample_pack"} {
		if !keys[want] {
			t.Errorf("no price %s", want)
		}
	}
	if keys["craftmarket/browser/month"] || keys["craftmarket/listing"] {
		t.Error("a free plan or a seller-set product has a provider price")
	}
	again := payforge.ProviderPlanFor(load(t, "marketplace.json"))
	a, _ := json.Marshal(plan)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatal("the provider plan is not stable")
	}
}
