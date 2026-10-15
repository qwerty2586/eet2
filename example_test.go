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
	// The certificate can live anywhere, e.g. in a database: its content is enough.
	p12, err := os.ReadFile("CA_EETPlayground-CZ00000019.p12")
	if err != nil {
		log.Fatal(err)
	}
	d, err := eet2.NewDispatcherFromP12(eet2.PlaygroundService, p12, "password")
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
		// No POK: queue the sale and send it again, within 48 hours.
		r.Resend = true
		resp, err = d.SendPayment(r)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("POK:", resp.Pok)
}

func ExampleDispatcher_RenewCertificate() {
	p12, _ := os.ReadFile("register.p12")
	d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, "password")
	if err != nil {
		log.Fatal(err)
	}

	// Renew 2–3 weeks before expiry, while the old certificate is still valid.
	if time.Until(d.Certificate().NotAfter) > 21*24*time.Hour {
		return
	}
	ctx := context.Background()

	// Files a request (or resumes an unfinished one), waits for the
	// certificate to be issued and downloads it.
	renewal, err := d.RenewCertificate(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// 1. Store the new certificate safely first. If this fails nothing is
	//    lost: the next RenewCertificate downloads the same certificate again.
	//    Keep the password (renewal.Password) separately, e.g. in a secrets vault.
	if err := os.WriteFile("register-new.p12", renewal.P12, 0o600); err != nil {
		log.Fatal(err)
	}

	// 2. Only then confirm receipt. This switches the Dispatcher to the new
	//    certificate; afterwards the CA no longer offers it for download.
	if err := renewal.Confirm(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("new certificate valid until", d.Certificate().NotAfter)
}
