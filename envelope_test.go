package eet2

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// officialSample načte vzorovou zprávu finanční správy
// (https://eet.gov.cz/assets/cs/cmsmedia/pro-vyvojare/CZ00000019.eet.v4.req.xml).
func officialSample(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/CZ00000019.eet.v4.req.xml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func extract(t *testing.T, s, pattern string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if m == nil {
		t.Fatalf("vzor %q nenalezen", pattern)
	}
	return m[1]
}

// Atributy Hlavicka a Data ze vzorové zprávy, přesně jak jsou v ní uvedeny.
var sampleHlavicka = []attr{
	{"uuid_zpravy", "03965780-6457-4842-bd80-5f9195c0b8c8"},
	{"dat_odesl", "2026-07-01T09:02:18Z"},
	{"prvni_zaslani", "true"},
	{"overeni", "false"},
}

var sampleData = []attr{
	{"eic_popl", "CZ00000019"},
	{"id_jednotky", "303"},
	{"id_pokl", "/5604/MA65"},
	{"porad_cis", "00/2224/SO57"},
	{"dat_trzby", "2026-07-01T09:02:18Z"},
	{"celk_trzba", "188580.00"},
	{"urceno_cerp_zuct", "25.00"},
	{"cerp_zuct", "302.00"},
}

// Kanonizace Body musí dát přesně DigestValue z oficiální vzorové zprávy.
func TestCanonicalBodyMatchesOfficialDigest(t *testing.T) {
	s := officialSample(t)
	bodyID := extract(t, s, `<soapenv:Body wsu:Id="([^"]+)"`)
	want := extract(t, s, `<ds:DigestValue>([^<]+)</ds:DigestValue>`)

	got := b64sha256(canonicalBody(bodyID, trzbaXML(sampleHlavicka, sampleData)))
	if got != want {
		t.Fatalf("DigestValue = %s, oficiální vzor má %s", got, want)
	}
}

// Kanonický SignedInfo musí odpovídat podpisu z oficiální vzorové zprávy,
// ověřenému veřejným klíčem certifikátu, který je v ní přiložen.
func TestCanonicalSignedInfoMatchesOfficialSignature(t *testing.T) {
	s := officialSample(t)
	bodyID := extract(t, s, `<soapenv:Body wsu:Id="([^"]+)"`)
	digest := extract(t, s, `<ds:DigestValue>([^<]+)</ds:DigestValue>`)
	sigB64 := extract(t, s, `<ds:SignatureValue>([^<]+)</ds:SignatureValue>`)
	certB64 := extract(t, s, `<wsse:BinarySecurityToken[^>]*>([^<]+)</wsse:BinarySecurityToken>`)

	der, err := base64.StdEncoding.DecodeString(certB64)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(canonicalSignedInfo(bodyID, digest)))
	if err := rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("podpis oficiální zprávy nesedí na náš kanonický SignedInfo: %v", err)
	}
}

// Vytvořená zpráva musí být well-formed XML, obsahovat certifikát a mít
// platný podpis nad kanonickým SignedInfo s digestem kanonického Body.
func TestBuildEnvelopeSelfConsistent(t *testing.T) {
	cert, key := testCert(t, "CZ00000019")
	r := testReceipt()
	h, d, err := r.attrs(time.Date(2027, 1, 8, 21, 19, 40, 0, prague))
	if err != nil {
		t.Fatal(err)
	}
	out, err := buildEnvelope(h, d, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	if err := xml.Unmarshal(out, new(struct{})); err != nil {
		t.Fatalf("výstup není well-formed XML: %v", err)
	}
	if len(out) > maxMessageSize {
		t.Fatalf("zpráva má %d B, limit je %d B", len(out), maxMessageSize)
	}
	for _, forbidden := range []string{"Timestamp", "wsa:", "\n"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("zpráva nemá obsahovat %q", forbidden)
		}
	}

	bodyID := extract(t, s, `<soapenv:Body xmlns:wsu="[^"]+" wsu:Id="([^"]+)"`)
	trzba := extract(t, s, `(<v4:Trzba>.*</v4:Trzba>)`)
	digest := extract(t, s, `<ds:DigestValue>([^<]+)</ds:DigestValue>`)
	if want := b64sha256(canonicalBody(bodyID, trzba)); digest != want {
		t.Fatalf("DigestValue %s, očekáváno %s", digest, want)
	}
	if ref := extract(t, s, `<ds:Reference URI="#([^"]+)">`); ref != bodyID {
		t.Fatalf("Reference URI #%s neukazuje na Body %s", ref, bodyID)
	}

	sig, _ := base64.StdEncoding.DecodeString(extract(t, s, `<ds:SignatureValue>([^<]+)</ds:SignatureValue>`))
	hs := sha256.Sum256([]byte(canonicalSignedInfo(bodyID, digest)))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hs[:], sig); err != nil {
		t.Fatalf("podpis nesedí: %v", err)
	}

	certDER, _ := base64.StdEncoding.DecodeString(extract(t, s, `<wsse:BinarySecurityToken[^>]*>([^<]+)<`))
	if string(certDER) != string(cert.Raw) {
		t.Fatal("BinarySecurityToken neobsahuje pokladní certifikát")
	}
	tokenID := extract(t, s, `<wsse:BinarySecurityToken[^>]* wsu:Id="([^"]+)"`)
	if ref := extract(t, s, `<wsse:Reference URI="#([^"]+)"`); ref != tokenID {
		t.Fatalf("KeyInfo odkazuje na #%s, token má Id %s", ref, tokenID)
	}

	wantData := `<v4:Data celk_trzba="250.00" dat_trzby="2027-01-08T21:19:38+01:00" eic_popl="CZ00000019" ` +
		`id_jednotky="181" id_pokl="RECEPCE" porad_cis="2027/000123"></v4:Data>`
	if !strings.Contains(s, wantData) {
		t.Fatalf("element Data neodpovídá:\n%s", trzba)
	}
}

func TestEscapeAttr(t *testing.T) {
	if got := escapeAttr("a&b<c\"d\te\nf\rg"); got != "a&amp;b&lt;c&quot;d&#x9;e&#xA;f&#xD;g" {
		t.Fatal(got)
	}
	if got := escapeAttr("RECEPCE 1/A"); got != "RECEPCE 1/A" {
		t.Fatal(got)
	}
}
