# Examples

Runnable examples, each with a test. None needs a Stripe account.

| Example | Shows |
|---------|-------|
| [`catalog`](catalog) | A pricing spec, and syncing it to Stripe: a dry run by default, `-apply` to write |
| [`pricingpage`](pricingpage) | A pricing page drawn from the spec, so it cannot disagree with billing |
| [`webhook`](webhook) | A webhook endpoint: verify, hand to the lifecycle, answer so Stripe retries only what a retry can fix |
| [`limits`](limits) | Enforcing a plan's limits and checking a feature level |

The catalog in [`catalog/pricing.json`](catalog/pricing.json) is the spec the
other examples read.

```bash
go run ./examples/catalog -spec examples/catalog/pricing.json
go run ./examples/pricingpage -spec examples/catalog/pricing.json
```
