package eet2_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/qwerty2586/eet2"
	"github.com/shopspring/decimal"
)

func Example() {
	// Certifikát může být uložený třeba v databázi – stačí jeho obsah.
	p12, err := os.ReadFile("CA_EETPlayground-CZ00000019.p12")
	if err != nil {
		log.Fatal(err)
	}
	d, err := eet2.NewDispatcherFromP12(eet2.PlaygroundService, p12, "heslo")
	if err != nil {
		log.Fatal(err)
	}

	r := eet2.Receipt{
		EicPopl:    "CZ00000019",
		IdJednotky: 303,
		IdPokl:     "RECEPCE",
		PoradCis:   "2027/000123",
		DatTrzby:   time.Now(),
		CelkTrzba:  decimal.RequireFromString("250.00"),
	}
	resp, err := d.SendPayment(r)
	if eet2.IsRetryable(err) {
		// Bez POK: uložit a odeslat znovu, nejpozději do 48 hodin.
		r.Opakovane = true
		resp, err = d.SendPayment(r)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("POK:", resp.Pok)
}

func ExampleDispatcher_RenewCertificate() {
	p12, _ := os.ReadFile("pokladna.p12")
	d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, "heslo")
	if err != nil {
		log.Fatal(err)
	}

	// Obnovit 2–3 týdny před koncem platnosti, dokud starý certifikát platí.
	if time.Until(d.Certificate().NotAfter) > 21*24*time.Hour {
		return
	}
	ctx := context.Background()

	// Podá žádost (nebo naváže na rozpracovanou), počká na vydání a stáhne certifikát.
	renewal, err := d.RenewCertificate(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// 1. Nejdřív nový certifikát bezpečně uložit. Když se to nepovede, nic se
	//    neztratí: příští RenewCertificate stáhne tentýž certifikát znovu.
	//    Heslo (renewal.Heslo) uložte zvlášť, např. do trezoru tajemství.
	if err := os.WriteFile("pokladna-nova.p12", renewal.P12, 0o600); err != nil {
		log.Fatal(err)
	}

	// 2. Teprve pak potvrdit převzetí. Dispatcher se tím přepne na nový
	//    certifikát; po potvrzení už ho u CA znovu stáhnout nejde.
	if err := renewal.Confirm(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("nový certifikát platí do", d.Certificate().NotAfter)
}
