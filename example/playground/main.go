// Program playground odešle jednu testovací tržbu do neprodukčního
// prostředí EET 2.0 (playground) a vypíše odpověď.
//
// Testovací certifikáty (CA_EETPlayground-*.p12) jsou v balíčku
// CAEET_Playground_2026_v1.zip na https://eet.gov.cz/cs/pro-vyvojare/dokumenty-ke-stazeni
//
//	go run ./example/playground -p12 CA_EETPlayground-CZ00000019.p12 -heslo <heslo>
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/qwerty2586/eet2"
)

func main() {
	p12 := flag.String("p12", "", "cesta k testovacímu certifikátu .p12")
	heslo := flag.String("heslo", "", "heslo k certifikátu")
	eic := flag.String("eic", "CZ00000019", "EIČ z certifikátu")
	jednotka := flag.Int("jednotka", 303, "číslo evidenční jednotky")
	overeni := flag.Bool("overeni", false, "ověřovací mód (tržba se neeviduje)")
	flag.Parse()
	if *p12 == "" {
		flag.Usage()
		return
	}

	d, err := eet2.NewDispatcher(eet2.PlaygroundService, *p12, *heslo)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("certifikát:", d.Certificate().Subject, "platí do", d.Certificate().NotAfter.Format("2006-01-02"))

	resp, err := d.SendPayment(eet2.Receipt{
		EicPopl:    *eic,
		IdJednotky: *jednotka,
		IdPokl:     "TEST1",
		PoradCis:   fmt.Sprintf("T%d", time.Now().Unix()),
		DatTrzby:   time.Now(),
		CelkTrzba:  123.45,
		Overeni:    *overeni,
	})
	if err != nil {
		log.Fatalf("chyba: %v (opakovat: %v)", err, eet2.IsRetryable(err))
	}
	fmt.Printf("UUID:     %s\n", resp.UuidZpravy)
	fmt.Printf("POK:      %s\n", resp.Pok)
	fmt.Printf("ověřeno:  %v, test: %v\n", resp.Overeno, resp.Test)
	for _, v := range resp.Varovani {
		fmt.Printf("varování: %d %s\n", v.Kod, v.Text)
	}
	fmt.Println("X-Global-Transaction-Id:", resp.GlobalTransactionID)
}
