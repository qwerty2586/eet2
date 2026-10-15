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

	"github.com/shopspring/decimal"
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
		CelkTrzba:  decimal.NewFromInt(250),
	}
}

func TestFormatAmount(t *testing.T) {
	ok := map[string]string{
		"0":            "0.00",
		"-0":           "0.00", // never "-0.00"
		"0.00":         "0.00",
		"250":          "250.00",
		"81.6":         "81.60",
		"81.60":        "81.60",
		"81.600":       "81.60", // trailing zeros are fine
		"0.56":         "0.56",
		"-187.2":       "-187.20",
		"-0.01":        "-0.01",
		"20.45":        "20.45",
		"0020.45":      "20.45", // no leading zeros
		"99999999.99":  "99999999.99",
		"-99999999.99": "-99999999.99",
	}
	for in, want := range ok {
		got, err := formatAmount(decimal.RequireFromString(in))
		if err != nil || got != want {
			t.Errorf("formatAmount(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"100000000", "-100000000", "1e300", "1.005", "12.345", "-0.001", "0.1234"} {
		if got, err := formatAmount(decimal.RequireFromString(in)); err == nil {
			t.Errorf("formatAmount(%s) = %q, want an error", in, got)
		}
	}
}

// The rounding recipe from docs/amounts.md.
func TestDocsRounding(t *testing.T) {
	price := decimal.RequireFromString("10.15")
	withVAT := price.Mul(decimal.RequireFromString("1.21")) // 12.2815
	if _, err := formatAmount(withVAT); err == nil {
		t.Fatal("an unrounded amount should be rejected")
	}
	cases := []struct {
		name string
		got  decimal.Decimal
		want string
	}{
		{"Round(2)", withVAT.Round(2), "12.28"},
		{"Round(2) half", decimal.RequireFromString("12.345").Round(2), "12.35"},
		{"Round(2) negative half", decimal.RequireFromString("-12.345").Round(2), "-12.35"},
		{"RoundBank(2)", decimal.RequireFromString("12.345").RoundBank(2), "12.34"},
		{"Round(0) cash", decimal.RequireFromString("81.60").Round(0), "82.00"},
		{"RoundFloor(0) cash down", decimal.RequireFromString("24.80").RoundFloor(0), "24.00"},
		{"NewFromFloat", decimal.NewFromFloat(1.005).Round(2), "1.01"},
		{"Round(0) total", withVAT.Round(2).Round(0), "12.00"},
		{"RoundCeil(0)", decimal.RequireFromString("24.20").RoundCeil(0), "25.00"},
		{"RoundFloor(0) negative", decimal.RequireFromString("-24.80").RoundFloor(0), "-25.00"},
		{"hellers", decimal.New(25050, -2), "250.50"},
		{"whole crowns", decimal.NewFromInt(250), "250.00"},
	}
	for _, c := range cases {
		got, err := formatAmount(c.got)
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestFormatTime(t *testing.T) {
	winter := time.Date(2027, 1, 9, 3, 25, 28, 0, time.UTC)
	if got := formatTime(winter); got != "2027-01-09T04:25:28+01:00" {
		t.Errorf("winter time: %s", got)
	}
	summer := time.Date(2027, 6, 9, 3, 25, 28, 0, time.UTC)
	if got := formatTime(summer); got != "2027-06-09T05:25:28+02:00" {
		t.Errorf("summer time: %s", got)
	}

	// Fractional seconds are truncated, never rounded up.
	frac := time.Date(2027, 1, 8, 20, 19, 38, 999_999_999, time.UTC)
	if got := formatTime(frac); got != "2027-01-08T21:19:38+01:00" {
		t.Errorf("fractional seconds: %s", got)
	}

	// The repeated hour when clocks go back (25 Oct 2026, 03:00 CEST -> 02:00 CET).
	first := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	second := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)
	if a, b := formatTime(first), formatTime(second); a != "2026-10-25T02:30:00+02:00" || b != "2026-10-25T02:30:00+01:00" {
		t.Errorf("repeated hour: %s, %s", a, b)
	}

	// The system time zone does not matter.
	saved := time.Local
	defer func() { time.Local = saved }()
	for _, name := range []string{"UTC", "America/New_York", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		time.Local = loc
		if got := formatTime(winter.In(time.Local)); got != "2027-01-09T04:25:28+01:00" {
			t.Errorf("time.Local=%s: %s", name, got)
		}
	}
}

func TestNewUUID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u := NewUUID()
		if !reUUID.MatchString(u) || u[14] != '4' {
			t.Fatalf("invalid v4 UUID: %s", u)
		}
		if seen[u] {
			t.Fatal("duplicate UUID")
		}
		seen[u] = true
	}
}

func TestValidate(t *testing.T) {
	if err := testReceipt().Validate(); err != nil {
		t.Fatalf("valid sale: %v", err)
	}
	bad := map[string]func(*Receipt){
		"eic_popl":         func(r *Receipt) { r.EicPopl = "12345678" },
		"eic_poverujiciho": func(r *Receipt) { r.EicPoverujiciho = "CZ1" },
		"id_jednotky":      func(r *Receipt) { r.IdJednotky = 0 },
		"id_pokl":          func(r *Receipt) { r.IdPokl = "Pokladna č. 1" },
		"porad_cis":        func(r *Receipt) { r.PoradCis = "" },
		"dat_trzby":        func(r *Receipt) { r.DatTrzby = time.Time{} },
		"celk_trzba":       func(r *Receipt) { r.CelkTrzba = decimal.RequireFromString("12.345") },
		"cerp_zuct":        func(r *Receipt) { r.CerpZuct = decimal.NewNullDecimal(decimal.NewFromInt(-1e9)) },
		"uuid_zpravy":      func(r *Receipt) { r.UuidZpravy = "abc" },
	}
	for field, mutate := range bad {
		r := testReceipt()
		mutate(&r)
		var ve *ValidationError
		if err := r.Validate(); !errors.As(err, &ve) || ve.Field != field {
			t.Errorf("%s: want ValidationError for %s, got %v", field, field, err)
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
	r.UrcenoCerpZuct = decimal.NewNullDecimal(decimal.NewFromInt(343))
	r.CerpZuct = decimal.NewNullDecimal(decimal.NewFromInt(237))
	h, d, err := r.attrs(time.Date(2027, 1, 8, 21, 19, 40, 0, prague))
	if err != nil {
		t.Fatal(err)
	}
	gotH := element("v4:Hlavicka", h)
	wantH := `<v4:Hlavicka dat_odesl="2027-01-08T21:19:40+01:00" overeni="true" prvni_zaslani="false" ` +
		`uuid_zpravy="e23e5a5a-08d7-4a08-844d-2b6c6b60621d"></v4:Hlavicka>`
	if gotH != wantH {
		t.Errorf("Hlavicka:\n%s\nwant\n%s", gotH, wantH)
	}
	gotD := element("v4:Data", d)
	wantD := `<v4:Data celk_trzba="250.00" cerp_zuct="237.00" dat_trzby="2027-01-08T21:19:38+01:00" ` +
		`eic_popl="CZ00000019" eic_poverujiciho="CZ8551015704" id_jednotky="181" id_pokl="RECEPCE" ` +
		`porad_cis="2027/000123" povereni_vice_popl="true" urceno_cerp_zuct="343.00"></v4:Data>`
	if gotD != wantD {
		t.Errorf("Data:\n%s\nwant\n%s", gotD, wantD)
	}

	// Optional fields without a value are omitted entirely.
	h, d, _ = testReceipt().attrs(time.Now())
	if len(h) != 3 || len(d) != 6 {
		t.Errorf("a minimal sale has 3 header and 6 data attributes, got %d and %d", len(h), len(d))
	}
}

func TestUniqueKey(t *testing.T) {
	k, err := testReceipt().UniqueKey()
	if err != nil {
		t.Fatal(err)
	}
	if want := "CZ00000019|181|RECEPCE|2027/000123|2027-01-08T21:19:38+01:00|250.00"; k != want {
		t.Fatalf("%s, want %s", k, want)
	}
}

func TestRegisterKey(t *testing.T) {
	r := testReceipt()
	k, err := r.RegisterKey()
	if err != nil {
		t.Fatal(err)
	}
	if want := "CZ00000019*181*RECEPCE*2027-01-08T21:19:38+01:00"; k != want {
		t.Fatalf("%s, want %s", k, want)
	}

	// The same second in another time zone and with milliseconds is the same key,
	// because the server sees the same dat_trzby.
	same := r
	same.DatTrzby = r.DatTrzby.UTC().Add(750 * time.Millisecond)
	if k2, _ := same.RegisterKey(); k2 != k {
		t.Errorf("same second: %s != %s", k2, k)
	}

	// Sequence number and amount do not matter; register and second do.
	other := r
	other.PoradCis, other.CelkTrzba = "2027/000999", decimal.NewFromInt(1)
	if k2, _ := other.RegisterKey(); k2 != k {
		t.Errorf("porad_cis and amount must not affect the key: %s", k2)
	}
	for name, mutate := range map[string]func(*Receipt){
		"next second":    func(r *Receipt) { r.DatTrzby = r.DatTrzby.Add(time.Second) },
		"other register": func(r *Receipt) { r.IdPokl = "ORD1" },
		"other unit":     func(r *Receipt) { r.IdJednotky = 182 },
	} {
		x := r
		mutate(&x)
		if k2, _ := x.RegisterKey(); k2 == k {
			t.Errorf("%s: key must differ", name)
		}
	}

	// Invalid fields are rejected, so "*" can never sneak into a field.
	bad := r
	bad.IdPokl = "A*B"
	if _, err := bad.RegisterKey(); err == nil {
		t.Error("id_pokl with * must be rejected")
	}
	bad = r
	bad.DatTrzby = time.Time{}
	if _, err := bad.RegisterKey(); err == nil {
		t.Error("missing dat_trzby must be rejected")
	}
}
