// Command playground sends one test sale to the EET 2.0 non-production
// environment (playground) and prints the reply.
//
// Test certificates (CA_EETPlayground-*.p12) are in the
// CAEET_Playground_2026_v1.zip bundle at https://eet.gov.cz/cs/pro-vyvojare/dokumenty-ke-stazeni
//
//	go run ./example/playground -p12 CA_EETPlayground-CZ00000019.p12 -password <password>
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/qwerty2586/eet2"
	"github.com/shopspring/decimal"
)

func main() {
	p12 := flag.String("p12", "", "path to the test certificate (.p12)")
	password := flag.String("password", "", "certificate password")
	eic := flag.String("eic", "CZ00000019", "EIČ from the certificate")
	unit := flag.Int("unit", 303, "registration unit number (id_jednotky)")
	verify := flag.Bool("verify", false, "verification mode (the sale is not registered)")
	flag.Parse()
	if *p12 == "" {
		flag.Usage()
		return
	}

	d, err := eet2.NewDispatcher(eet2.PlaygroundService, *p12, *password)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("certificate:", d.Certificate().Subject, "valid until", d.Certificate().NotAfter.Format("2006-01-02"))

	resp, err := d.SendPayment(eet2.Receipt{
		EicPopl:    *eic,
		IdJednotky: *unit,
		IdPokl:     "TEST1",
		PoradCis:   fmt.Sprintf("T%d", time.Now().Unix()),
		DatTrzby:   time.Now(),
		CelkTrzba:  decimal.RequireFromString("123.45"),
		Overeni:    *verify,
	})
	if err != nil {
		log.Fatalf("error: %v (retryable: %v)", err, eet2.IsRetryable(err))
	}
	fmt.Printf("UUID:     %s\n", resp.UuidZpravy)
	fmt.Printf("POK:      %s\n", resp.Pok)
	fmt.Printf("verified: %v, test: %v\n", resp.Verified, resp.Test)
	for _, w := range resp.Warnings {
		fmt.Printf("warning:  %d %s\n", w.Code, w.Text)
	}
	fmt.Println("X-Global-Transaction-Id:", resp.GlobalTransactionID)
}
