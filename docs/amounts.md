# Amounts and rounding

Amounts are [`decimal.Decimal`](https://github.com/shopspring/decimal), not `float64`. A float
cannot represent most decimal numbers exactly (`1.005` is really `1.00499…`), so the registered
amount could end up a heller off from the receipt.

**The library does not round.** An amount with more than two decimal places is rejected with
`*eet2.ValidationError`. EET must receive the amount actually paid, i.e. the one your application
prints on the receipt or charges to the card. Rounding therefore belongs in the application,
**once and in one place**, and the same value is then used for the receipt, the payment and EET.

## Recommended approach

```go
// 1. Calculate in decimal throughout, never via float64.
price := decimal.RequireFromString("10.15")
withVAT := price.Mul(decimal.RequireFromString("1.21")) // 12.2815

// 2. Round the amount due to hellers.
//    Round rounds half away from zero: 12.345 → 12.35, -12.345 → -12.35.
total := withVAT.Round(2) // 12.28

// 3. Cash payment: the customer pays whole crowns, register what was actually paid.
cash := total.Round(0) // 12.00   (81.60 → 82.00)

// 4. Use the same value on the receipt and in EET.
r.CelkTrzba = cash // or total for a card payment
```

## Rounding methods

| Method | Example | When |
|---|---|---|
| `Round(2)` | 12.345 → 12.35 | regular rounding to hellers |
| `RoundBank(2)` | 12.345 → 12.34 | banker's rounding, if your accounting uses it |
| `Round(0)` | 81.60 → 82.00 | cash in whole crowns by the usual rules |
| `RoundFloor(0)` | 24.80 → 24.00 | cash always down, in the customer's favour |
| `RoundCeil(0)` | 24.20 → 25.00 | always up |

Watch out for negative amounts on refunds: `RoundFloor` and `RoundCeil` round towards minus/plus
infinity, so `RoundFloor(0)` turns `-24.80` into `-25.00`. For symmetric rounding of refunds use
`Round`, or round the absolute value and restore the sign.

## Creating amounts

```go
decimal.RequireFromString("250.50")  // a literal or text, e.g. from a form
decimal.NewFromInt(250)              // whole crowns
decimal.New(25050, -2)               // hellers: 25050 × 10⁻² = 250.50
decimal.NewFromFloat(x).Round(2)     // if you have a float, round right away
```

`decimal.Decimal` implements `sql.Scanner`, so it can be read directly from a
`DECIMAL(12,2)` / `NUMERIC(12,2)` column.

Optional amounts (`UrcenoCerpZuct`, `CerpZuct`) are `decimal.NullDecimal`:

```go
r.UrcenoCerpZuct = decimal.NewNullDecimal(decimal.NewFromInt(1000)) // topping up credit
```

Every example on this page is also covered by a test (`TestDocsRounding`).
