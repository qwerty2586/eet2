package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qwerty2586/eet2"
	"software.sslmate.com/src/go-pkcs12"
)

func newP12(t *testing.T, pass string) ([]byte, *x509.Certificate) {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{CommonName: "CZ00000019"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	p12, err := pkcs12.LegacyDES.Encode(key, cert, nil, pass)
	if err != nil {
		t.Fatal(err)
	}
	return p12, cert
}

func TestSaveAndResume(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nova.p12")
	p12, cert := newP12(t, "ABcd1234")
	ren := &eet2.Renewal{P12: p12, Heslo: "ABcd1234", Certificate: cert}

	if ok, err := alreadySaved(out, ren); ok || err != nil {
		t.Fatalf("prázdný adresář: %v %v", ok, err)
	}
	if err := save(out+".heslo", []byte(ren.Heslo+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := save(out, ren.P12); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
		t.Fatalf("práva %v, chci 0600", fi.Mode().Perm())
	}
	if ok, err := alreadySaved(out, ren); !ok || err != nil {
		t.Fatalf("uložený certifikát nerozpoznán: %v %v", ok, err)
	}

	// Po přerušení vrátí CA tentýž certifikát v jinak zašifrovaném souboru.
	ren2 := &eet2.Renewal{P12: []byte("jiný obsah"), Heslo: "Zz998877", Certificate: cert}
	if ok, err := alreadySaved(out, ren2); !ok || err != nil {
		t.Fatalf("tentýž certifikát má projít i s jiným souborem: %v %v", ok, err)
	}

	// Jiný certifikát se nikdy nepřepíše.
	_, other := newP12(t, "x")
	if ok, err := alreadySaved(out, &eet2.Renewal{Certificate: other}); ok || err == nil {
		t.Fatal("soubor s jiným certifikátem se nemá považovat za uložený")
	}
}
