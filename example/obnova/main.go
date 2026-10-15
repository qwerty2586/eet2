// Program obnova obnoví pokladní certifikát přes API CA EET.
//
// Nový certifikát uloží do souboru (a heslo k němu vedle) a teprve potom
// potvrdí CA jeho převzetí. Když se cokoli pokazí před potvrzením, stačí
// program spustit znovu: naváže na tutéž žádost a stáhne tentýž certifikát.
//
//	go run ./example/obnova -p12 pokladna.p12 -heslo <heslo> -out pokladna-2028.p12
//
// Nový certifikát: pokladna-2028.p12, heslo: pokladna-2028.p12.heslo
// (oba soubory jen pro vlastníka, 0600).
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
	p12 := flag.String("p12", "", "stávající pokladní certifikát .p12")
	heslo := flag.String("heslo", "", "heslo ke stávajícímu certifikátu")
	out := flag.String("out", "", "kam uložit nový certifikát .p12 (heslo se uloží do <out>.heslo)")
	ca := flag.String("ca", "prod", "prostředí CA EET: prod, test, zkus")
	dni := flag.Int("dni", 21, "obnovit, jen pokud certifikát vyprší do tolika dní (0 = vždy)")
	flag.Parse()
	if *p12 == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	services := map[string]eet2.CAService{
		"prod": eet2.CAProductionService,
		"test": eet2.CATestService,
		"zkus": eet2.CAZkusebniService,
	}
	caService, ok := services[*ca]
	if !ok {
		log.Fatalf("neznámé prostředí CA %q", *ca)
	}

	d, err := eet2.NewDispatcher(eet2.ProductionService, *p12, *heslo, eet2.WithCAService(caService))
	if err != nil {
		log.Fatal(err)
	}
	old := d.Certificate()
	fmt.Printf("stávající certifikát: %s, platí do %s\n", old.Subject.CommonName, old.NotAfter.Format("2006-01-02 15:04"))
	if time.Now().After(old.NotAfter) {
		log.Fatal("certifikát už vypršel – obnovit ho nelze, nový získáte v DIS+ na portálu MOJE daně")
	}
	if left := time.Until(old.NotAfter); *dni > 0 && left > time.Duration(*dni)*24*time.Hour {
		fmt.Printf("do konce platnosti zbývá %d dní, obnova zatím není potřeba (viz -dni)\n", int(left.Hours()/24))
		return
	}

	// Ctrl+C přeruší čekání; žádost u CA běží dál a příští spuštění na ni naváže.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	fmt.Println("žádám o nový certifikát…")
	renewal, err := d.RenewCertificate(ctx)
	if err != nil {
		var rej *eet2.RejectedError
		if errors.As(err, &rej) {
			log.Fatalf("CA žádost zamítla: %s", rej.Reason)
		}
		log.Fatal(err)
	}
	fmt.Printf("vydán: %s, platí do %s (žádost %s)\n",
		renewal.Certificate.Subject.CommonName, renewal.Certificate.NotAfter.Format("2006-01-02 15:04"), renewal.ReqID)

	// 1. Uložit. Dokud se to nepovede, převzetí se nepotvrdí.
	saved, err := alreadySaved(*out, renewal)
	if err != nil {
		log.Fatalf("%v (převzetí nepotvrzeno)", err)
	}
	if saved {
		fmt.Printf("certifikát už je uložený v %s z předchozího spuštění\n", *out)
	} else {
		if err := save(*out+".heslo", []byte(renewal.Heslo+"\n")); err != nil {
			log.Fatalf("uložení hesla: %v (převzetí nepotvrzeno, spusťte znovu)", err)
		}
		if err := save(*out, renewal.P12); err != nil {
			log.Fatalf("uložení certifikátu: %v (převzetí nepotvrzeno, spusťte znovu)", err)
		}
		fmt.Printf("uloženo: %s a %s.heslo\n", *out, *out)
	}

	// Kontrola, že uložený soubor jde opravdu otevřít a je to ten nový certifikát.
	if ok, err := alreadySaved(*out, renewal); err != nil || !ok {
		log.Fatalf("uložený certifikát nejde ověřit: %v (převzetí nepotvrzeno)", err)
	}

	// 2. Teprve teď potvrdit převzetí.
	if err := renewal.Confirm(ctx); err != nil {
		log.Fatalf("potvrzení převzetí: %v (certifikát je uložený, spusťte znovu)", err)
	}
	fmt.Println("převzetí potvrzeno, nový certifikát je aktivní")
}

// alreadySaved zjistí, jestli v souboru path (s heslem v path.heslo) už je
// nový certifikát, např. z přerušeného předchozího spuštění. Soubor s jiným
// certifikátem nikdy nepřepíše.
func alreadySaved(path string, renewal *eet2.Renewal) (bool, error) {
	p12, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	heslo, err := os.ReadFile(path + ".heslo")
	if err != nil {
		return false, fmt.Errorf("soubor %s existuje, ale nejde přečíst heslo: %w", path, err)
	}
	d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, strings.TrimSpace(string(heslo)))
	if err != nil {
		return false, fmt.Errorf("soubor %s existuje, ale nejde otevřít: %w; zvolte jiné -out", path, err)
	}
	if !d.Certificate().Equal(renewal.Certificate) {
		return false, fmt.Errorf("soubor %s obsahuje jiný certifikát, zvolte jiné -out", path)
	}
	return true, nil
}

// save zapíše soubor jen pro vlastníka přes dočasný soubor, aby nevznikl
// napůl zapsaný soubor.
func save(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obnova-*")
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
