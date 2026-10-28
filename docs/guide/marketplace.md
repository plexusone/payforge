# Marketplaces

A spec with a `marketplace` section lets your sellers be paid directly, with the
platform keeping a fee. Each seller has a Stripe **Express** account: Stripe hosts
the onboarding and holds the seller's identity and bank details.

```go
var connect payforge.Connect = client // *stripe.Client implements it
```

## Onboarding a seller

```go
acct, err := connect.CreateAccount(ctx, seller, "US") // store acct on the seller
link, err := connect.OnboardingLink(ctx, acct, refreshURL, returnURL)
// redirect the seller to link
```

- `CreateAccount` makes one account per seller; a repeat returns the same one.
  Stripe cannot search accounts, so **store the ID**. The country is the
  seller's two-letter code and cannot be changed later.
- An onboarding link is single-use and expires in minutes. Stripe sends the
  seller to `refreshURL` when it has expired; that page should call
  `OnboardingLink` again.
- `Account(ctx, id)` reads the account: `ChargesEnabled`, `PayoutsEnabled`,
  `DetailsSubmitted`. Returning to `returnURL` does **not** mean onboarding
  finished; check, or wait for the `account.updated` event.

## A sale

```go
sess, err := connect.CheckoutSale(ctx, payforge.Sale{
    Buyer:         buyer,
    SellerAccount: acct,
    SellerPlan:    "seller",
    Product:       "listing",
    AmountCents:   2599, // for a seller-set product
    Reference:     "order-8841",
    SuccessURL:    okURL,
    CancelURL:     cancelURL,
})
```

It is a destination charge made on the seller's behalf: the seller is the
merchant on the buyer's statement, the money is transferred to the seller's
account, and the platform fee is taken as the application fee.

- **The fee** is the seller plan's `platformFeeBps` when it sets one, otherwise
  the marketplace's, applied to what the buyer pays and **rounded down** to a cent.
  A sale of 499 cents at 8% charges 39 cents, not 40. A fee of zero sends no
  application fee. `payforge.FeeBps` and `payforge.Fee` compute it for a pricing
  page.
- **A seller who cannot take charges** is refused with `ErrSellerNotReady` before
  any checkout is made. This reads the account, one API call per sale.
- **An account this app did not create** is refused with `ErrForeignAccount`. The
  money goes to the account named, so an account ID from a request, or a wrong
  record, cannot pay another app's seller on the same Stripe account.
- **`SellerPlan` is trusted.** It chooses the fee, so take it from your own record
  of the seller, never from a request.
- For a fixed-price product the fee is computed from the spec's amount. Keep the
  catalog [synced](catalog.md#syncing), or Stripe refuses a fee larger than a
  lower price.
- A spec without a marketplace cannot create accounts or sell
  (`ErrNoMarketplace`).
- The fee is on the amount the seller set. Shipping or tax added at checkout is
  not part of it.

## Refunds and account changes

Two more ports receive what happens afterwards. Both are optional; without one,
its events are ignored (and not recorded, so you can add it later).

```go
life := &payforge.Lifecycle{
    Spec: spec, Store: store, Sink: sink, Provider: client,
    Sales:    saleSink,   // refunds
    Sellers:  sellerSink, // connected-account changes
    Accounts: client,     // read the account's current state
}
```

```go
type SaleSink interface {
    ApplyRefund(ctx context.Context, r payforge.SaleRefund) error
}
type SellerSink interface {
    ApplySeller(ctx context.Context, a payforge.SellerAccount) error
}
```

- **Refunds.** `SaleRefund` names the buyer, seller, product, charge and payment
  intent (to match the order you recorded) and the **total refunded so far**. A
  full refund (`Full`) means the buyer no longer has access to what was bought;
  keep the data. The total only grows, so keep the highest you have seen and
  treat a full refund as permanent: a late event for an earlier partial refund
  must not reopen the sale. Refunds of a subscription's invoice, or of another
  app's charge, are skipped.
- **Accounts.** `SellerAccount` is the whole current state. With `Accounts` set,
  the lifecycle applies what Stripe says now, not what the event says, so a late
  event cannot turn charges back on. A seller can lose the ability to sell after
  onboarding; use `ChargesEnabled` to decide whether to show their listings.
  Accounts payforge did not create are skipped.

`payforgetest.Sales` and `payforgetest.Sellers` check your implementations, as
[`payforgetest.Store`](lifecycle.md#checking-your-own-implementations) does.

## Sellers outside your Stripe region

`CreateAccount` requests both the `card_payments` and `transfers` capabilities,
which is what a sale made on the seller's behalf needs. Stripe supports that for
sellers in the platform's own region. Sellers elsewhere are onboarded under a
different agreement with `transfers` only, and a sale on their behalf is not
possible as written. Treat cross-border sellers as unsupported.

## Not covered

- A one-time sale being paid. Record it yourself when your checkout completes
  (`EventCheckoutCompleted` with `Paid`), and match a later refund by payment
  intent.
- Delayed payment methods that settle days later: the success arrives as
  `EventCheckoutCompleted` with `Paid` and the failure as `EventCheckoutFailed`;
  both are yours to record.
- Disputes reach a `DisputeSink` by charge, not by buyer; see
  [disputes](lifecycle.md#disputes-and-failed-payments).
- Payouts and transfer reversals; Stripe handles them and sends events payforge
  ignores.
