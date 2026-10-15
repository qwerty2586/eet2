# Cash register certificate

## Loading

The certificate does not have to be a file. This helps when it lives in a database or a secrets vault:

```go
// PKCS#12 file (.p12 / .pfx)
d, err := eet2.NewDispatcher(eet2.ProductionService, "register.p12", password)

// PKCS#12 as []byte
d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12Bytes, password)

// PEM: certificate and key (PKCS#1 or PKCS#8), possibly in one block
d, err := eet2.NewDispatcherFromPEM(eet2.ProductionService, certPEM, keyPEM)

// your own crypto.Signer (HSM, cloud KMS)
d, err := eet2.NewDispatcherWithSigner(eet2.ProductionService, cert, signer)
```

From a string, `[]byte(s)` is enough. For Base64 use `base64.StdEncoding.DecodeString(s)`.

## Checking a certificate and its password

`CheckP12` checks a `.p12` without creating a Dispatcher: the password, that it holds an RSA key
and that the key matches the certificate. It works offline and returns the certificate, so you
can show its EIČ and validity, e.g. when a user uploads a certificate:

```go
cert, err := eet2.CheckP12(p12, password)
switch {
case errors.Is(err, eet2.ErrIncorrectPassword):
	// wrong password
case err != nil:
	// corrupt file, missing key, key does not match the certificate…
default:
	fmt.Println(cert.Subject.CommonName, "valid until", cert.NotAfter) // CZ00000019 valid until …
}
```

The Dispatcher constructors (`NewDispatcher`, `NewDispatcherFromP12`, `UpdateCertificateFromP12`)
return the same `ErrIncorrectPassword` for a wrong password.

## Renewal through the CA EET API

A cash register certificate is valid for 366 days. The Dispatcher can renew it through the EET
certificate authority API. It signs in with the current certificate, so nothing else is needed.
Renew **while the old certificate is still valid**; the tax administration recommends
2–3 weeks ahead.

A ready-made program that renews the certificate and stores it in a file is in
[`example/certificate_renewal`](../example/certificate_renewal):

```sh
go run ./example/certificate_renewal -p12 register.p12 -password <password> -out register-new.p12
```

In code (see also `ExampleDispatcher_RenewCertificate` on pkg.go.dev):

```go
renewal, err := d.RenewCertificate(ctx) // files a request, waits for issue, downloads
// … store renewal.P12 and renewal.Password …
err = renewal.Confirm(ctx)              // confirms receipt and switches the Dispatcher to the new certificate
```

Order matters: **after `Confirm` the CA no longer offers the certificate for download.** That is
why the library never confirms on its own and the Dispatcher keeps signing with the old
certificate until `Confirm`.

What happens when something fails:

- **The application crashes or `ctx` expires at any point before `Confirm`:** the request stays
  at the CA. The next `RenewCertificate` call finds it and downloads the same certificate again
  instead of filing a new request. The CA keeps an unclaimed certificate for 30 days after issue.
- **`Confirm` is never called:** the Dispatcher stays on the old certificate and after 30 days the
  CA deletes the download. A new `RenewCertificate` call fixes that.
- **The reply to `Confirm` gets lost:** calling `Confirm` again is safe.
- **The old certificate expired in the meantime:** `Confirm` signs in with the new one.
- **The CA rejects the request:** you get `*eet2.RejectedError` with the reason.
- **CA API error:** you get `*eet2.CAError`. `Temporary()` is `true` for rate limiting (HTTP 429)
  and server errors; `RetryAfter` says when to try again.

`P12` and `Password` are sensitive: do not log them or store them unencrypted.
`fmt.Print(renewal)` deliberately leaves them out.

### CA environments

`ProductionService` uses the production CA (`https://ca.caeet.gov.cz/api`) automatically.
Otherwise set it explicitly:

```go
d, err := eet2.NewDispatcherFromP12(eet2.PlaygroundService, p12, password,
	eet2.WithCAService(eet2.CATestService), // or eet2.CATrialService
)
```

The tax administration's documentation does not say which CA environment belongs to the
playground. The shared playground test certificates most likely cannot be renewed.

## Swapping at runtime

A certificate downloaded manually from DIS+ (the MOJE daně portal) can be swapped without a restart:

```go
err := d.UpdateCertificateFromP12(p12, password)
```

The swap is safe during concurrent sends. Sends in progress finish with the old certificate,
later ones use the new one. Sales waiting in a resend queue are therefore signed with the
currently valid certificate, as the specification requires.
