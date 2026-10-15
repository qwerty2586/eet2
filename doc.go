// Package eet2 is a client for the Czech electronic sales registration
// EET 2.0 (Act No. 180/2026 Coll., EET interface version 4).
//
// The package builds the sale data message, signs it with the cash register
// certificate per WS-Security (Exclusive C14N, SHA-256, RSA-SHA256), sends it
// synchronously to the tax administration and returns the confirmation code
// (POK) or an error. It can also renew the certificate through the CA EET API.
//
// Fields that map to EET message attributes keep their Czech names
// (EicPopl, PoradCis, CelkTrzba, Pok, …) so they match the official
// specification.
//
// Amounts are decimal.Decimal (github.com/shopspring/decimal) with at most
// two decimal places. Rounding is up to the application so that the
// registered amount matches the receipt.
//
// Basic usage:
//
//	d, err := eet2.NewDispatcherFromP12(eet2.PlaygroundService, p12Bytes, "password")
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
//			// queue the sale and send it again with Resend: true
//		}
//		log.Fatal(err)
//	}
//	fmt.Println("POK:", resp.Pok)
//
// Interface specification: https://eet.gov.cz/cs/pro-vyvojare
package eet2
