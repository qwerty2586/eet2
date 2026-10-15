# Jak je knihovna ověřená

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
- Obnova certifikátu se testuje proti simulované CA EET podle oficiální specifikace API
  (`caeetapi_jwt.yml`). Simulace ověřuje podpis JWT i otisk `x5t#S256` a pokrývá navázání
  po pádu, zamítnutí, ztracené potvrzení i vypršení starého certifikátu.
- Výměna certifikátu za běhu se testuje se souběžným odesíláním pod `go test -race`.

## Vyzkoušení na playgroundu

Testovací certifikáty jsou v balíčku `CAEET_Playground_2026_v1.zip`
([Dokumenty ke stažení](https://eet.gov.cz/cs/pro-vyvojare/dokumenty-ke-stazeni)):

```sh
go run ./example/playground -p12 CA_EETPlayground-CZ00000019.p12 -heslo <heslo>
```

Playground vrací POK končící na `-ff`, který není platným potvrzením.
