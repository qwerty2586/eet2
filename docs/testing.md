# How the library is verified

```sh
go test ./...
```

- Canonicalizing the message body gives exactly the `DigestValue` of the tax administration's
  official sample message
  ([CZ00000019.eet.v4.req.xml](https://eet.gov.cz/assets/cs/cmsmedia/pro-vyvojare/CZ00000019.eet.v4.req.xml)).
- The canonical `SignedInfo` matches the signature in the same sample message, verified with the
  certificate attached to it.
- Generated messages were checked during development with an independent Exclusive C14N
  implementation (libxml2/lxml) and `openssl dgst -verify`.
- Sending, error codes, timeouts and certificate loading (PKCS#12, PEM) are tested against a
  local test server.
- Certificate renewal is tested against a simulated CA EET built from the official API
  specification (`caeetapi_jwt.yml`). The simulation verifies the JWT signature and the
  `x5t#S256` thumbprint, and covers resuming after a crash, rejection, a lost confirmation and
  an expired old certificate.
- Swapping the certificate at runtime is tested with concurrent sends under `go test -race`.
- **Sending was verified against the live EET 2.0 playground** with `example/playground` and an
  official test certificate: the server accepted the signed message and returned a test POK
  ending in `-ff`.

## Trying it on the playground

Test certificates are in the `CAEET_Playground_2026_v1.zip` bundle
([Dokumenty ke stažení](https://eet.gov.cz/cs/pro-vyvojare/dokumenty-ke-stazeni)):

```sh
go run ./example/playground -p12 CA_EETPlayground-CZ00000019.p12 -password <password>
```

The playground returns a POK ending in `-ff`, which is not a valid confirmation.
