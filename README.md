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
		CelkTrzba:  250.00,
	}
	resp, err := d.SendPayment(r)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("POK:", resp.Pok)
}
```

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
| `CelkTrzba` | `celk_trzba` | ano | částka v Kč, u storna záporná |
| `EicPoverujiciho` | `eic_poverujiciho` | – | evidence v pověření |
| `PovereniVicePopl` | `povereni_vice_popl` | – | tržba plyne více poplatníkům |
| `UrcenoCerpZuct` | `urceno_cerp_zuct` | – | nabití kreditu, `eet2.Castka(1000)` |
| `CerpZuct` | `cerp_zuct` | – | čerpání kreditu, `eet2.Castka(290)` |
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
