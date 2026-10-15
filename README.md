# eet2

[![Go](https://github.com/qwerty2586/eet2/actions/workflows/go.yml/badge.svg)](https://github.com/qwerty2586/eet2/actions/workflows/go.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/qwerty2586/eet2.svg)](https://pkg.go.dev/github.com/qwerty2586/eet2)

Go klient pro českou **elektronickou evidenci tržeb EET 2.0**, která platí od 1. 1. 2027
(zákon č. 180/2026 Sb., rozhraní EET verze 4).

Knihovna sestaví datovou zprávu o tržbě, podepíše ji pokladním certifikátem
(WS-Security, Exclusive C14N, SHA-256, RSA-SHA256), odešle ji a vrátí potvrzovací kód (POK).

> **Tuto knihovnu vygeneroval [Claude](https://claude.ai) (Anthropic)** na základě oficiální
> technické dokumentace finanční správy. Kód je otestovaný (viz [Testy](#testy)),
> ale před nasazením do produkce si ho prosím projděte a vyzkoušejte na playgroundu.

## Instalace

```sh
go get github.com/qwerty2586/eet2
```

## Použití

```go
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/qwerty2586/eet2"
	"github.com/shopspring/decimal"
)

func main() {
	d, err := eet2.NewDispatcher(eet2.PlaygroundService,
		"CA_EETPlayground-CZ00000019.p12",
		"heslo",
	)
	if err != nil {
		log.Fatal(err)
	}

	r := eet2.Receipt{
		EicPopl:    "CZ00000019",
		IdJednotky: 303,
		IdPokl:     "RECEPCE",
		PoradCis:   "2027/000123",
		DatTrzby:   time.Now(),
		CelkTrzba:  decimal.RequireFromString("250.00"),
	}
	resp, err := d.SendPayment(r)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("POK:", resp.Pok)
}
```

### Částky a zaokrouhlení

Částky jsou typu [`decimal.Decimal`](https://github.com/shopspring/decimal), ne `float64`.
Float neumí přesně vyjádřit většinu desetinných čísel (`1.005` je ve skutečnosti `1.00499…`),
takže by evidovaná částka mohla vyjít o haléř jinak než na dokladu.

**Knihovna nezaokrouhluje.** Částku s víc než dvěma desetinnými místy odmítne jako
`*eet2.ValidationError`. Do EET se má evidovat skutečně přijatá částka, tedy ta, kterou vaše
aplikace vytiskne na doklad nebo stáhne z karty. Zaokrouhlení proto musí proběhnout v aplikaci,
**jednou a na jednom místě**, a stejnou hodnotu pak použít pro doklad, platbu i EET.

Doporučený postup:

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

Další možnosti zaokrouhlení:

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

Odkud částku vzít:

```go
decimal.RequireFromString("250.50")  // literál nebo text, např. z formuláře
decimal.NewFromInt(250)              // celé koruny
decimal.New(25050, -2)               // haléře: 25050 × 10⁻² = 250.50
decimal.NewFromFloat(x).Round(2)     // máte-li float, zaokrouhlete hned při převodu
```

`decimal.Decimal` implementuje `sql.Scanner`, takže se dá načíst přímo ze sloupce
`DECIMAL(12,2)` / `NUMERIC(12,2)`.

### Certifikát z paměti

Certifikát nemusí ležet v souboru. Hodí se, když ho máte v databázi nebo v trezoru tajemství:

```go
// PKCS#12 (.p12 / .pfx) jako []byte
d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12Bytes, heslo)

// PEM: certifikát a klíč (PKCS#1 nebo PKCS#8), klidně v jednom bloku
d, err := eet2.NewDispatcherFromPEM(eet2.ProductionService, certPEM, keyPEM)

// vlastní crypto.Signer (HSM, cloudový KMS)
d, err := eet2.NewDispatcherWithSigner(eet2.ProductionService, cert, signer)
```

Z řetězce stačí převod `[]byte(s)`. Pro Base64 použijte `base64.StdEncoding.DecodeString(s)`.

### Výpadek spojení a opakované odeslání

Komunikace je synchronní (request/response). Když odpověď nepřijde, nelze zjistit, jestli server
tržbu přijal. Tržbu je pak potřeba odeslat znovu, **se stejnými daty** a příznakem `Opakovane`.
Nové UUID se vygeneruje samo. Server opakované odeslání téže tržby pozná a nezapočítá ji dvakrát.

```go
resp, err := d.SendPayment(r)
if eet2.IsRetryable(err) {
	// výpadek sítě, timeout nebo dočasná chyba serveru (kód -1):
	// uložte r do fronty a odešlete znovu bez zbytečného odkladu,
	// nejpozději do 48 hodin
	r.Opakovane = true
	queue.Push(r)
}
```

`IsRetryable` vrací `false` pro chyby validace a kritické chyby zprávy (`*eet2.Error` s kladným
kódem). Ty opakováním nezmizí, je potřeba opravit data nebo nastavení.

Jako klíč fronty můžete použít `r.UniqueKey()`. Je to stejná kombinace údajů, podle které
tržbu rozpoznává server.

### Ověřovací mód

```go
r.Overeni = true
resp, err := d.SendPayment(r) // resp.Overeno == true, tržba se NEeviduje
```

### Nastavení

```go
d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, heslo,
	eet2.WithTimeout(5*time.Second),  // mezní doba odezvy, minimum 2 s (výchozí 3 s)
	eet2.WithHTTPClient(myClient),    // vlastní HTTP klient
)
```

## Údaje tržby

| Pole | XML | Povinné | Popis |
|---|---|---|---|
| `EicPopl` | `eic_popl` | ano | EIČ poplatníka, `CZ` + 8–10 číslic |
| `IdJednotky` | `id_jednotky` | ano | číslo evidenční jednotky z DIS+ |
| `IdPokl` | `id_pokl` | ano | označení pokladního zařízení, 1–20 znaků |
| `PoradCis` | `porad_cis` | ano | pořadové číslo tržby, 1–25 znaků |
| `DatTrzby` | `dat_trzby` | ano | čas tržby, odešle se v zóně Europe/Prague |
| `CelkTrzba` | `celk_trzba` | ano | `decimal.Decimal` v Kč, max. 2 desetinná místa, u storna záporná |
| `EicPoverujiciho` | `eic_poverujiciho` | – | evidence v pověření |
| `PovereniVicePopl` | `povereni_vice_popl` | – | tržba plyne více poplatníkům |
| `UrcenoCerpZuct` | `urceno_cerp_zuct` | – | nabití kreditu, `decimal.NewNullDecimal(decimal.NewFromInt(1000))` |
| `CerpZuct` | `cerp_zuct` | – | čerpání kreditu, `decimal.NewNullDecimal(decimal.NewFromInt(290))` |
| `Opakovane` | `prvni_zaslani` | – | opakované zaslání |
| `Overeni` | `overeni` | – | ověřovací mód |
| `UuidZpravy` | `uuid_zpravy` | – | prázdné = vygeneruje se |

Údaje se před odesláním zkontrolují proti formátům ze specifikace. Neplatná tržba se neodešle
a vrátí `*eet2.ValidationError`. Server totiž na některé vadné zprávy neodpovídá vůbec.

## Testy

```sh
go test ./...
```

- Kanonizace těla zprávy dává přesně `DigestValue` z oficiální vzorové zprávy finanční správy
  ([CZ00000019.eet.v4.req.xml](https://eet.gov.cz/assets/cs/cmsmedia/pro-vyvojare/CZ00000019.eet.v4.req.xml)).
- Kanonický `SignedInfo` odpovídá podpisu z téže vzorové zprávy, ověřenému přiloženým certifikátem.
- Vytvořené zprávy byly při vývoji ověřeny nezávislou implementací Exclusive C14N (libxml2/lxml)
  a `openssl dgst -verify`.
- Odesílání, chybové kódy, timeouty a načítání certifikátů (PKCS#12, PEM) se testují proti
  lokálnímu testovacímu serveru.

Proti skutečnému playgroundu si knihovnu vyzkoušíte testovacím certifikátem z balíčku
`CAEET_Playground_2026_v1.zip` ([Dokumenty ke stažení](https://eet.gov.cz/cs/pro-vyvojare/dokumenty-ke-stazeni)):

```sh
go run ./example/playground -p12 CA_EETPlayground-CZ00000019.p12 -heslo <heslo>
```

## Omezení

- Elektronický podpis potvrzovací zprávy se zatím neověřuje. Pravost odpovědi zajišťuje
  TLS spojení s ověřeným certifikátem serveru.
- Knihovna neřeší frontu pro opakované odesílání ani ukládání tržeb. To patří do aplikace.

## Inspirace

- [prochac/eet](https://github.com/prochac/eet): Go klient pro původní EET (2016–2022).
  Jednoduchost jeho API (`NewDispatcher`, `Receipt`, `SendPayment`) je přímým vzorem pro tuto knihovnu.

## Zdroje

- [EET 2.0 – Pro vývojáře](https://eet.gov.cz/cs/pro-vyvojare) (finanční správa)
- Formát a struktura údajů o evidované tržbě a popis datového rozhraní, verze 1.2
- Popis položek datové zprávy s příklady jejich vyplnění, verze 1.0
- Přístupové a provozní informace pro playground a produkci, verze 1.1

Knihovna není oficiálním produktem finanční správy a nijak s ní nesouvisí.

## Licence

[MIT](LICENSE)
