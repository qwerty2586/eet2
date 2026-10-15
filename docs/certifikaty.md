# Pokladní certifikát

## Načtení

Certifikát nemusí ležet v souboru. Hodí se, když ho máte v databázi nebo v trezoru tajemství:

```go
// soubor PKCS#12 (.p12 / .pfx)
d, err := eet2.NewDispatcher(eet2.ProductionService, "pokladna.p12", heslo)

// PKCS#12 jako []byte
d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12Bytes, heslo)

// PEM: certifikát a klíč (PKCS#1 nebo PKCS#8), klidně v jednom bloku
d, err := eet2.NewDispatcherFromPEM(eet2.ProductionService, certPEM, keyPEM)

// vlastní crypto.Signer (HSM, cloudový KMS)
d, err := eet2.NewDispatcherWithSigner(eet2.ProductionService, cert, signer)
```

Z řetězce stačí převod `[]byte(s)`. Pro Base64 použijte `base64.StdEncoding.DecodeString(s)`.

## Obnova přes API CA EET

Pokladní certifikát platí 366 dní. Dispatcher ho umí obnovit přes API certifikační autority EET.
Přihlásí se stávajícím certifikátem, takže nic dalšího nepotřebujete. Obnovit je nutné,
**dokud starý certifikát ještě platí**. Finanční správa doporučuje 2–3 týdny předem.

Hotový program, který certifikát obnoví a uloží do souboru, je v [`example/obnova`](../example/obnova):

```sh
go run ./example/obnova -p12 pokladna.p12 -heslo <heslo> -out pokladna-nova.p12
```

V kódu (viz též `ExampleDispatcher_RenewCertificate` na pkg.go.dev):

```go
renewal, err := d.RenewCertificate(ctx) // podá žádost, počká na vydání, stáhne
// … uložit renewal.P12 a renewal.Heslo …
err = renewal.Confirm(ctx)              // potvrdí převzetí a přepne Dispatcher na nový certifikát
```

Na pořadí záleží: **po `Confirm` už certifikát u CA znovu stáhnout nejde.** Proto knihovna
převzetí nikdy nepotvrzuje sama a Dispatcher až do `Confirm` dál podepisuje starým certifikátem.

Co se stane, když něco selže:

- **Pád aplikace nebo vypršení `ctx` kdykoli před `Confirm`:** žádost u CA zůstává. Další volání
  `RenewCertificate` ji najde a stáhne tentýž certifikát znovu, novou žádost nepodává.
  Nevyzvednutý certifikát CA drží 30 dní od vydání.
- **`Confirm` zapomenete zavolat:** Dispatcher zůstane u starého certifikátu a po 30 dnech CA
  stažená data smaže. Nový `RenewCertificate` to napraví.
- **Odpověď na `Confirm` se ztratí po cestě:** opakované `Confirm` je bezpečné.
- **Starý certifikát mezitím vypršel:** `Confirm` se přihlásí novým.
- **CA žádost zamítne:** vrátí se `*eet2.RejectedError` s důvodem.
- **Chyba API CA:** vrátí se `*eet2.CAError`. `Temporary()` je `true` u přetížení (HTTP 429)
  a chyb serveru, `RetryAfter` říká, za jak dlouho to zkusit znovu.

`P12` a `Heslo` jsou citlivé údaje: nelogujte je a neukládejte nešifrovaně. `fmt.Print(renewal)`
je záměrně nevypisuje.

### Prostředí CA

Pro `ProductionService` se použije produkční CA (`https://ca.caeet.gov.cz/api`) automaticky.
Jinak ji nastavte ručně:

```go
d, err := eet2.NewDispatcherFromP12(eet2.PlaygroundService, p12, heslo,
	eet2.WithCAService(eet2.CATestService), // nebo eet2.CAZkusebniService
)
```

Dokumentace finanční správy neuvádí, které prostředí CA odpovídá playgroundu. Sdílené
testovací certifikáty playgroundu se obnovit nejspíš nedají.

## Ruční výměna za běhu

Certifikát stažený ručně z DIS+ (portál MOJE daně) vyměníte bez restartu:

```go
err := d.UpdateCertificateFromP12(p12, heslo)
```

Výměna je bezpečná i při souběžném odesílání. Rozpracovaná odeslání doběhnou se starým
certifikátem, další použijí nový. Tržby čekající ve frontě na opakované odeslání se tak
podepíšou aktuálně platným certifikátem, jak to specifikace vyžaduje.
