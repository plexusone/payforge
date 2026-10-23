// Command catalog loads a pricing spec and shows what syncing it to Stripe
// would do, or does it.
//
//	go run ./examples/catalog -spec examples/catalog/pricing.json
//	STRIPE_SECRET_KEY=sk_test_... go run ./examples/catalog -spec examples/catalog/pricing.json
//	STRIPE_SECRET_KEY=sk_test_... go run ./examples/catalog -spec examples/catalog/pricing.json -apply
//
// Without a key the spec is checked and nothing else happens: that is free
// mode. With one, the default is a dry run that changes nothing.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/plexusone/payforge/stripe"
	"github.com/plexusone/systemspec-pricingpackaging/pricingspec"
)

func main() {
	path := flag.String("spec", "pricing.json", "the pricing spec")
	apply := flag.Bool("apply", false, "write to Stripe instead of only showing what would change")
	flag.Parse()
	if err := run(context.Background(), *path, os.Getenv("STRIPE_SECRET_KEY"), *apply, "", os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run checks the spec and, with a key, syncs it. apiURL replaces Stripe's
// address and is empty outside tests.
func run(ctx context.Context, path, key string, apply bool, apiURL string, out io.Writer) error {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the command's argument
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only file
	spec, err := pricingspec.Load(f)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d plans, %d products\n", spec.App, len(spec.Plans), len(spec.Products))
	if key == "" {
		fmt.Fprintln(out, "no STRIPE_SECRET_KEY: free mode, nothing to sync")
		return nil
	}
	c, err := stripe.New(spec, stripe.Config{SecretKey: key, APIURL: apiURL})
	if err != nil {
		return err
	}
	report, err := c.Sync(ctx, !apply)
	for _, a := range report.Actions {
		fmt.Fprintf(out, "%-14s %s  %s\n", a.Kind, a.Key, a.Detail)
	}
	if err != nil {
		return err
	}
	switch {
	case !report.Changed():
		fmt.Fprintln(out, "Stripe already matches the spec")
	case report.DryRun:
		fmt.Fprintln(out, "dry run: nothing was written; add -apply to write")
	default:
		fmt.Fprintln(out, "synced")
	}
	return nil
}
