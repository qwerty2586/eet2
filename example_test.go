package eet2_test

import (
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
