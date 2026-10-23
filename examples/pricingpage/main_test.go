package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

func load(t *testing.T) *pricingspec.Spec {
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
	return spec
}

func TestThePageShowsWhatTheSpecSays(t *testing.T) {
	var out bytes.Buffer
	if err := Render(&out, load(t)); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"<th>Plus</th>", "$12.00/month", "$120.00/year", "(14-day trial)", "Free", "Contact us", "unlimited"} {
		if !strings.Contains(got, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestTextFromTheSpecIsEscaped(t *testing.T) {
	spec := load(t)
	spec.Plans[0].Name = "<script>alert(1)</script>"
	var out bytes.Buffer
	if err := Render(&out, spec); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<script>") {
		t.Fatal("a plan name was written unescaped")
	}
}
