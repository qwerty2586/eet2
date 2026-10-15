// Package eet2 je klient pro českou elektronickou evidenci tržeb EET 2.0
// (zákon č. 180/2026 Sb., rozhraní EET verze 4).
//
// Balíček sestaví datovou zprávu o evidované tržbě, podepíše ji pokladním
// certifikátem podle WS-Security (Exclusive C14N, SHA-256, RSA-SHA256),
// odešle ji synchronně na společné technické zařízení správce daně
// a vrátí potvrzovací kód (POK) nebo chybu.
//
// Částky jsou decimal.Decimal (github.com/shopspring/decimal) a smí mít
// nejvýše dvě desetinná místa. Zaokrouhlení je na aplikaci, aby evidovaná
// částka odpovídala dokladu.
//
// Základní použití:
//
//	d, err := eet2.NewDispatcherFromP12(eet2.PlaygroundService, p12Bytes, "heslo")
//	if err != nil {
//		log.Fatal(err)
//	}
//	resp, err := d.SendPayment(eet2.Receipt{
//		EicPopl:    "CZ00000019",
//		IdJednotky: 303,
//		IdPokl:     "RECEPCE",
//		PoradCis:   "2027/000123",
//		DatTrzby:   time.Now(),
//		CelkTrzba:  decimal.RequireFromString("250.00"),
//	})
//	if err != nil {
//		if eet2.IsRetryable(err) {
//			// uložit do fronty a odeslat znovu s Opakovane: true
//		}
//		log.Fatal(err)
//	}
//	fmt.Println("POK:", resp.Pok)
//
// Specifikace rozhraní: https://eet.gov.cz/cs/pro-vyvojare
package eet2
