# The catalog

The pricing spec is a JSON document. This page covers only what payforge reads;
the format is described in the
[spec repository](https://github.com/plexusone/systemspec-pricingpackaging), and
a complete example is [`examples/catalog/pricing.json`](../../examples/catalog/pricing.json).

## What becomes what

| In the spec | At Stripe |
|-------------|-----------|
| A plan with prices | One product, and one recurring price per interval |
| A plan with no prices (free or arranged) | Nothing: it is never sold through checkout |
| A one-time product with an `amountCents` | One product, and one price |
| A seller-set product | A product only; the amount is chosen at checkout |

A plan is sold through checkout only if it is `selfServe` and has a price for the
interval. An arranged plan such as Enterprise is set up by a person, not by a
buyer.

## Lookup keys

Every price payforge creates carries a **lookup key**, and everything finds
prices by it, never by Stripe's price ID:

- A plan's price: `<app>/<plan>/<interval>`, for example `taskboard/plus/month`.
- A product's price: `<app>/<product>`.

Product IDs are chosen by payforge too, as `payforge_<app>_<plan>`. Lookup keys
are why one Stripe account can serve several products: each has its own `app`.
Objects carry `payforge_app`, `payforge_plan`, `payforge_product`,
`payforge_subject` and `payforge_seller` metadata, which is how an event is
matched to the account it belongs to.

## Syncing

Prices must exist before anyone can check out, so sync the catalog to the account
first. Always look before you write:

```go
report, err := client.Sync(ctx, true) // true: a dry run, nothing is written
for _, a := range report.Actions {
    fmt.Println(a.Kind, a.Key, a.Detail)
}
if err != nil {
    return err // the report lists what happened before the failure
}
if report.Changed() {
    _, err = client.Sync(ctx, false)
}
```

The actions are `create_product`, `update_product`, `create_price`,
`replace_price` and `unchanged`. Sync follows four rules:

- **It is safe to repeat.** A second run changes nothing.
- **It never deletes.** A product or price that is no longer in the spec stays at
  Stripe, so existing subscribers keep what they pay for.
- **It never edits an amount.** Stripe prices are immutable. When the spec's
  amount, currency or interval changes, sync creates a new price, moves the
  lookup key to it and leaves the old price for the subscribers already on it.
- **It reports a failure with what happened before it.** Run it again after
  fixing the cause.
- **It refuses what is not its own.** A product at Stripe whose metadata names
  another app is `stripe.ErrForeign`, and an archived one is `stripe.ErrArchived`;
  un-archive it or choose another plan ID. A **deleted** product is
  `stripe.ErrDeleted`; its ID can never be used again, so the plan needs a new
  ID in the spec.

The command-line version is [`examples/catalog`](../../examples/catalog); it runs
a dry run unless given `-apply`.

## Changing the catalog

| You change | What happens |
|------------|--------------|
| A plan's price | A new price; subscribers stay on the old one until you move them |
| A plan's name or features | The product's name is updated; features live in the spec, not at Stripe |
| A plan's **ID** | A **new product** and new prices. The old ones stay; sync does not rename them, and existing subscribers keep their old price, which the spec no longer names. Avoid renaming plan IDs; add a new plan and retire the old one instead |
| A plan removed from the spec | Nothing at Stripe. A subscription on a price the spec does not know **stops granting** that plan |

The last row is deliberate: only a price the spec has can grant a plan, so a
price made by hand in the Stripe dashboard can never give anyone Enterprise. See
[the lifecycle](lifecycle.md#what-grants-a-plan).

## Accounts that already have objects

If the Stripe account was used before payforge, subscriptions and customers
exist with metadata of the host's own naming. Give the webhook reader and the
lifecycle the prefix the host used (`LegacyPrefixes: []string{"myapp"}`) and
`myapp_organization_id` and `myapp_plan` are read as the subject and the plan.
A legacy subscription grants only a self-serve plan that has prices.
