// Command certificate_renewal renews the cash register certificate through
// the CA EET API.
//
// It stores the new certificate in a file (with its password next to it)
// and only then confirms receipt to the CA. If anything fails before the
// confirmation, just run it again: it resumes the same request and downloads
// the same certificate.
//
//	go run ./example/certificate_renewal -p12 register.p12 -password <password> -out register-2028.p12
//
// New certificate: register-2028.p12, password: register-2028.p12.password
// (both readable by the owner only, 0600).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/qwerty2586/eet2"
)

func main() {
	p12 := flag.String("p12", "", "current cash register certificate (.p12)")
	password := flag.String("password", "", "password of the current certificate")
	out := flag.String("out", "", "where to store the new .p12 (the password goes to <out>.password)")
	ca := flag.String("ca", "prod", "CA EET environment: prod, test, trial")
	days := flag.Int("days", 21, "renew only if the certificate expires within this many days (0 = always)")
	flag.Parse()
	if *p12 == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	services := map[string]eet2.CAService{
		"prod":  eet2.CAProductionService,
		"test":  eet2.CATestService,
		"trial": eet2.CATrialService,
	}
	caService, ok := services[*ca]
	if !ok {
		log.Fatalf("unknown CA environment %q", *ca)
	}

	d, err := eet2.NewDispatcher(eet2.ProductionService, *p12, *password, eet2.WithCAService(caService))
	if err != nil {
		log.Fatal(err)
	}
	old := d.Certificate()
	fmt.Printf("current certificate: %s, valid until %s\n", old.Subject.CommonName, old.NotAfter.Format("2006-01-02 15:04"))
	if time.Now().After(old.NotAfter) {
		log.Fatal("the certificate has expired and cannot be renewed; get a new one in DIS+ (MOJE daně)")
	}
	if left := time.Until(old.NotAfter); *days > 0 && left > time.Duration(*days)*24*time.Hour {
		fmt.Printf("%d days left, no renewal needed yet (see -days)\n", int(left.Hours()/24))
		return
	}

	// Ctrl+C stops waiting; the request keeps going at the CA and the next run resumes it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	fmt.Println("requesting a new certificate…")
	renewal, err := d.RenewCertificate(ctx)
	if err != nil {
		var rej *eet2.RejectedError
		if errors.As(err, &rej) {
			log.Fatalf("the CA rejected the request: %s", rej.Reason)
		}
		log.Fatal(err)
	}
	fmt.Printf("issued: %s, valid until %s (request %s)\n",
		renewal.Certificate.Subject.CommonName, renewal.Certificate.NotAfter.Format("2006-01-02 15:04"), renewal.ReqID)

	// 1. Store it. Receipt is not confirmed until this succeeds.
	saved, err := alreadySaved(*out, renewal)
	if err != nil {
		log.Fatalf("%v (receipt not confirmed)", err)
	}
	if saved {
		fmt.Printf("certificate already stored in %s by a previous run\n", *out)
	} else {
		if err := save(*out+".password", []byte(renewal.Password+"\n")); err != nil {
			log.Fatalf("storing password: %v (receipt not confirmed, run again)", err)
		}
		if err := save(*out, renewal.P12); err != nil {
			log.Fatalf("storing certificate: %v (receipt not confirmed, run again)", err)
		}
		fmt.Printf("stored: %s and %s.password\n", *out, *out)
	}

	// Check that the stored file really opens and holds the new certificate.
	if ok, err := alreadySaved(*out, renewal); err != nil || !ok {
		log.Fatalf("cannot verify the stored certificate: %v (receipt not confirmed)", err)
	}

	// 2. Only now confirm receipt.
	if err := renewal.Confirm(ctx); err != nil {
		log.Fatalf("confirming receipt: %v (certificate is stored, run again)", err)
	}
	fmt.Println("receipt confirmed, the new certificate is active")
}

// alreadySaved reports whether path (with the password in path.password)
// already holds the new certificate, e.g. from an interrupted previous run.
// A file with a different certificate is never overwritten.
func alreadySaved(path string, renewal *eet2.Renewal) (bool, error) {
	p12, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	password, err := os.ReadFile(path + ".password")
	if err != nil {
		return false, fmt.Errorf("%s exists but its password cannot be read: %w", path, err)
	}
	d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, strings.TrimSpace(string(password)))
	if err != nil {
		return false, fmt.Errorf("%s exists but cannot be opened: %w; choose another -out", path, err)
	}
	if !d.Certificate().Equal(renewal.Certificate) {
		return false, fmt.Errorf("%s holds a different certificate, choose another -out", path)
	}
	return true, nil
}

// save writes a file readable by the owner only, through a temporary file
// so that a half-written file never appears.
func save(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".certificate_renewal-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
