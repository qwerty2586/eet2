# eet2

[![Go](https://github.com/qwerty2586/eet2/actions/workflows/go.yml/badge.svg)](https://github.com/qwerty2586/eet2/actions/workflows/go.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/qwerty2586/eet2.svg)](https://pkg.go.dev/github.com/qwerty2586/eet2)

Go klient pro českou **elektronickou evidenci tržeb EET 2.0**, která platí od 1. 1. 2027
(zákon č. 180/2026 Sb., rozhraní EET verze 4). Sestaví datovou zprávu o tržbě, podepíše ji
pokladním certifikátem, odešle ji a vrátí potvrzovací kód (POK).

> **Tuto knihovnu vygeneroval [Claude](https://claude.ai) (Anthropic)** na základě oficiální
> technické dokumentace finanční správy. Kód je otestovaný ([jak](docs/testy.md)),
> ale před nasazením do produkce si ho prosím projděte a vyzkoušejte na playgroundu.

## Instalace

```sh
go get github.com/qwerty2586/eet2
```

## Použití

```go
d, err := eet2.NewDispatcher(eet2.PlaygroundService, "CA_EETPlayground-CZ00000019.p12", "heslo")
if err != nil {
	log.Fatal(err)
}

resp, err := d.SendPayment(eet2.Receipt{
	EicPopl:    "CZ00000019",
	IdJednotky: 303,
	IdPokl:     "RECEPCE",
	PoradCis:   "2027/000123",
	DatTrzby:   time.Now(),
	CelkTrzba:  decimal.RequireFromString("250.00"),
})
if err != nil {
	log.Fatal(err)
}
fmt.Println("POK:", resp.Pok)
```

## Co umí

- **Certifikát ze souboru i z paměti:** `.p12` jako `[]byte`, PEM nebo vlastní `crypto.Signer`
  (HSM, KMS). → [docs/certifikaty.md](docs/certifikaty.md)
- **Přesné částky:** `decimal.Decimal` místo `float64`. Knihovna nezaokrouhluje, částku s víc než
  dvěma desetinnými místy odmítne. → [docs/castky.md](docs/castky.md)
- **Opakované odeslání po výpadku:** `eet2.IsRetryable(err)` a `Opakovane: true`.
  → [docs/opakovani.md](docs/opakovani.md)
- **Obnova certifikátu přes API CA EET:** `RenewCertificate` a `Confirm`, výměna za běhu bez
  restartu. → [docs/certifikaty.md](docs/certifikaty.md), hotový program
  [example/obnova](example/obnova)
- **Kontrola údajů před odesláním** podle formátů ze specifikace. Popis položek tržby je
  v dokumentaci typu [`Receipt`](https://pkg.go.dev/github.com/qwerty2586/eet2#Receipt).

## Příklady

```sh
# odeslání testovací tržby na playground
go run ./example/playground -p12 CA_EETPlayground-CZ00000019.p12 -heslo <heslo>

# obnova pokladního certifikátu
go run ./example/obnova -p12 pokladna.p12 -heslo <heslo> -out pokladna-nova.p12
```

## Omezení

- Elektronický podpis potvrzovací zprávy se zatím neověřuje. Pravost odpovědi zajišťuje
  TLS spojení s ověřeným certifikátem serveru.
- Knihovna neřeší frontu pro opakované odesílání ani ukládání tržeb. To patří do aplikace.
- Obnova certifikátu zatím nebyla vyzkoušena proti skutečné CA EET, jen proti simulaci podle
  oficiální specifikace API.

## Inspirace

- [prochac/eet](https://github.com/prochac/eet): Go klient pro původní EET (2016–2022).
  Jednoduchost jeho API (`NewDispatcher`, `Receipt`, `SendPayment`) je přímým vzorem pro tuto knihovnu.

## Zdroje

- [EET 2.0 – Pro vývojáře](https://eet.gov.cz/cs/pro-vyvojare): technická specifikace rozhraní,
  popis položek datové zprávy, přístupové informace a dokumentace certifikační autority EET.

Knihovna není oficiálním produktem finanční správy a nijak s ní nesouvisí.

## Licence

[MIT](LICENSE)
