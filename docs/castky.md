# Částky a zaokrouhlení

Částky jsou typu [`decimal.Decimal`](https://github.com/shopspring/decimal), ne `float64`.
Float neumí přesně vyjádřit většinu desetinných čísel (`1.005` je ve skutečnosti `1.00499…`),
takže by evidovaná částka mohla vyjít o haléř jinak než na dokladu.

**Knihovna nezaokrouhluje.** Částku s víc než dvěma desetinnými místy odmítne jako
`*eet2.ValidationError`. Do EET se má evidovat skutečně přijatá částka, tedy ta, kterou vaše
aplikace vytiskne na doklad nebo stáhne z karty. Zaokrouhlení proto musí proběhnout v aplikaci,
**jednou a na jednom místě**, a stejnou hodnotu pak použít pro doklad, platbu i EET.

## Doporučený postup

```go
// 1. Počítejte celou dobu v decimal, nikdy přes float64.
cena := decimal.RequireFromString("10.15")
sDph := cena.Mul(decimal.RequireFromString("1.21")) // 12.2815

// 2. Výslednou částku k úhradě zaokrouhlete na haléře.
//    Round zaokrouhluje matematicky (půlka od nuly): 12.345 → 12.35, -12.345 → -12.35.
celkem := sDph.Round(2) // 12.28

// 3. Platba v hotovosti: zákazník platí celé koruny, evidujte, co skutečně zaplatil.
hotove := celkem.Round(0) // 12.00   (81.60 → 82.00)

// 4. Stejnou hodnotu použijte na doklad i do EET.
r.CelkTrzba = hotove // nebo celkem u platby kartou
```

## Metody zaokrouhlení

| Metoda | Příklad | Kdy |
|---|---|---|
| `Round(2)` | 12.345 → 12.35 | běžné zaokrouhlení na haléře |
| `RoundBank(2)` | 12.345 → 12.34 | bankéřské zaokrouhlení, pokud ho používá váš účetní systém |
| `Round(0)` | 81.60 → 82.00 | hotovost na celé koruny podle pravidel zaokrouhlování |
| `RoundFloor(0)` | 24.80 → 24.00 | hotovost vždy dolů ve prospěch zákazníka |
| `RoundCeil(0)` | 24.20 → 25.00 | vždy nahoru |

Pozor na zápornou částku u storna: `RoundFloor` a `RoundCeil` zaokrouhlují k mínus/plus
nekonečnu, takže `-24.80` se `RoundFloor(0)` zaokrouhlí na `-25.00`. Pro symetrické zaokrouhlení
storna použijte `Round`, nebo zaokrouhlete absolutní hodnotu a znaménko doplňte.

## Odkud částku vzít

```go
decimal.RequireFromString("250.50")  // literál nebo text, např. z formuláře
decimal.NewFromInt(250)              // celé koruny
decimal.New(25050, -2)               // haléře: 25050 × 10⁻² = 250.50
decimal.NewFromFloat(x).Round(2)     // máte-li float, zaokrouhlete hned při převodu
```

`decimal.Decimal` implementuje `sql.Scanner`, takže se dá načíst přímo ze sloupce
`DECIMAL(12,2)` / `NUMERIC(12,2)`.

Nepovinné částky (`UrcenoCerpZuct`, `CerpZuct`) jsou `decimal.NullDecimal`:

```go
r.UrcenoCerpZuct = decimal.NewNullDecimal(decimal.NewFromInt(1000)) // nabití kreditu
```

Všechny příklady z této stránky jsou zároveň v testech (`TestReadmeRounding`).
