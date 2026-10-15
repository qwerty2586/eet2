package eet2

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"
)

func testCert(t *testing.T, eic string) (*x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: eic, Country: []string{"CZ"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func testReceipt() Receipt {
	return Receipt{
		EicPopl:    "CZ00000019",
		IdJednotky: 181,
		IdPokl:     "RECEPCE",
		PoradCis:   "2027/000123",
		DatTrzby:   time.Date(2027, 1, 8, 21, 19, 38, 0, prague),
		CelkTrzba:  250,
	}
}

func TestFormatAmount(t *testing.T) {
	ok := map[float64]string{
		0:            "0.00",
		250:          "250.00",
		81.6:         "81.60",
		0.56:         "0.56",
		-187.2:       "-187.20",
		-0.01:        "-0.01",
		-0.001:       "0.00", // žádné "-0.00"
		20.45:        "20.45",
		0.1 + 0.2:    "0.30",
		1.005:        "1.00", // float 1.005 je ve skutečnosti 1.00499…
		99999999.99:  "99999999.99",
		-99999999.99: "-99999999.99",
	}
	for v, want := range ok {
		got, err := formatAmount(v)
		if err != nil || got != want {
			t.Errorf("formatAmount(%v) = %q, %v; chci %q", v, got, err, want)
		}
	}
	for _, v := range []float64{100000000, -100000000, 1e300} {
		if _, err := formatAmount(v); err == nil {
			t.Errorf("formatAmount(%v) měl vrátit chybu", v)
		}
	}
}

func TestFormatTime(t *testing.T) {
	winter := time.Date(2027, 1, 9, 3, 25, 28, 0, time.UTC)
	if got := formatTime(winter); got != "2027-01-09T04:25:28+01:00" {
		t.Errorf("zimní čas: %s", got)
	}
	summer := time.Date(2027, 6, 9, 3, 25, 28, 0, time.UTC)
	if got := formatTime(summer); got != "2027-06-09T05:25:28+02:00" {
		t.Errorf("letní čas: %s", got)
	}
}

func TestNewUUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u := NewUUID()
		if !reUUID.MatchString(u) || u[14] != '4' {
			t.Fatalf("neplatné UUID v4: %s", u)
		}
		if seen[u] {
			t.Fatal("duplicitní UUID")
		}
		seen[u] = true
	}
}

func TestValidate(t *testing.T) {
	if err := testReceipt().Validate(); err != nil {
		t.Fatalf("platná tržba: %v", err)
	}
	bad := map[string]func(*Receipt){
		"eic_popl":         func(r *Receipt) { r.EicPopl = "12345678" },
		"eic_poverujiciho": func(r *Receipt) { r.EicPoverujiciho = "CZ1" },
		"id_jednotky":      func(r *Receipt) { r.IdJednotky = 0 },
		"id_pokl":          func(r *Receipt) { r.IdPokl = "Pokladna č. 1" },
		"porad_cis":        func(r *Receipt) { r.PoradCis = "" },
		"dat_trzby":        func(r *Receipt) { r.DatTrzby = time.Time{} },
		"celk_trzba":       func(r *Receipt) { r.CelkTrzba = 1e9 },
		"cerp_zuct":        func(r *Receipt) { r.CerpZuct = Castka(-1e9) },
		"uuid_zpravy":      func(r *Receipt) { r.UuidZpravy = "abc" },
	}
	for field, mutate := range bad {
		r := testReceipt()
		mutate(&r)
		var ve *ValidationError
		if err := r.Validate(); !errors.As(err, &ve) || ve.Field != field {
			t.Errorf("%s: chci ValidationError pro %s, mám %v", field, field, err)
		}
	}
}

func TestAttrsOptionalFields(t *testing.T) {
	r := testReceipt()
	r.UuidZpravy = "e23e5a5a-08d7-4a08-844d-2b6c6b60621d"
	r.Opakovane = true
	r.Overeni = true
	r.EicPoverujiciho = "CZ8551015704"
	r.PovereniVicePopl = true
	r.UrcenoCerpZuct = Castka(343)
	r.CerpZuct = Castka(237)
	h, d, err := r.attrs(time.Date(2027, 1, 8, 21, 19, 40, 0, prague))
	if err != nil {
		t.Fatal(err)
	}
	gotH := element("v4:Hlavicka", h)
	wantH := `<v4:Hlavicka dat_odesl="2027-01-08T21:19:40+01:00" overeni="true" prvni_zaslani="false" ` +
		`uuid_zpravy="e23e5a5a-08d7-4a08-844d-2b6c6b60621d"></v4:Hlavicka>`
	if gotH != wantH {
		t.Errorf("Hlavicka:\n%s\nchci\n%s", gotH, wantH)
	}
	gotD := element("v4:Data", d)
	wantD := `<v4:Data celk_trzba="250.00" cerp_zuct="237.00" dat_trzby="2027-01-08T21:19:38+01:00" ` +
		`eic_popl="CZ00000019" eic_poverujiciho="CZ8551015704" id_jednotky="181" id_pokl="RECEPCE" ` +
		`porad_cis="2027/000123" povereni_vice_popl="true" urceno_cerp_zuct="343.00"></v4:Data>`
	if gotD != wantD {
		t.Errorf("Data:\n%s\nchci\n%s", gotD, wantD)
	}

	// Nepovinné položky se bez hodnoty vůbec neuvádějí.
	h, d, _ = testReceipt().attrs(time.Now())
	if len(h) != 3 || len(d) != 6 {
		t.Errorf("minimální tržba má mít 3 atributy hlavičky a 6 dat, má %d a %d", len(h), len(d))
	}
}

func TestUniqueKey(t *testing.T) {
	k, err := testReceipt().UniqueKey()
	if err != nil {
		t.Fatal(err)
	}
	if want := "CZ00000019|181|RECEPCE|2027/000123|2027-01-08T21:19:38+01:00|250.00"; k != want {
		t.Fatalf("%s, chci %s", k, want)
	}
}
