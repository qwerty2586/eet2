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

// officialSample loads the tax administration's sample message
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
		t.Fatalf("pattern %q not found", pattern)
	}
	return m[1]
}

// Hlavicka and Data attributes from the sample message, exactly as they appear in it.
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

// Canonicalizing Body must give exactly the DigestValue of the official sample message.
func TestCanonicalBodyMatchesOfficialDigest(t *testing.T) {
	s := officialSample(t)
	bodyID := extract(t, s, `<soapenv:Body wsu:Id="([^"]+)"`)
	want := extract(t, s, `<ds:DigestValue>([^<]+)</ds:DigestValue>`)

	got := b64sha256(canonicalBody(bodyID, trzbaXML(sampleHlavicka, sampleData)))
	if got != want {
		t.Fatalf("DigestValue = %s, the official sample has %s", got, want)
	}
}

// The canonical SignedInfo must match the signature in the official sample
// message, verified with the public key of the certificate attached to it.
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
		t.Fatalf("the official signature does not match our canonical SignedInfo: %v", err)
	}
}

// The built message must be well-formed XML, contain the certificate and have
// a valid signature over the canonical SignedInfo with the canonical Body digest.
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
		t.Fatalf("output is not well-formed XML: %v", err)
	}
	if len(out) > maxMessageSize {
		t.Fatalf("message is %d B, the limit is %d B", len(out), maxMessageSize)
	}
	for _, forbidden := range []string{"Timestamp", "wsa:", "\n"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("message must not contain %q", forbidden)
		}
	}

	bodyID := extract(t, s, `<soapenv:Body xmlns:wsu="[^"]+" wsu:Id="([^"]+)"`)
	trzba := extract(t, s, `(<v4:Trzba>.*</v4:Trzba>)`)
	digest := extract(t, s, `<ds:DigestValue>([^<]+)</ds:DigestValue>`)
	if want := b64sha256(canonicalBody(bodyID, trzba)); digest != want {
		t.Fatalf("DigestValue %s, want %s", digest, want)
	}
	if ref := extract(t, s, `<ds:Reference URI="#([^"]+)">`); ref != bodyID {
		t.Fatalf("Reference URI #%s does not point to Body %s", ref, bodyID)
	}

	sig, _ := base64.StdEncoding.DecodeString(extract(t, s, `<ds:SignatureValue>([^<]+)</ds:SignatureValue>`))
	hs := sha256.Sum256([]byte(canonicalSignedInfo(bodyID, digest)))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hs[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}

	certDER, _ := base64.StdEncoding.DecodeString(extract(t, s, `<wsse:BinarySecurityToken[^>]*>([^<]+)<`))
	if string(certDER) != string(cert.Raw) {
		t.Fatal("BinarySecurityToken does not contain the certificate")
	}
	tokenID := extract(t, s, `<wsse:BinarySecurityToken[^>]* wsu:Id="([^"]+)"`)
	if ref := extract(t, s, `<wsse:Reference URI="#([^"]+)"`); ref != tokenID {
		t.Fatalf("KeyInfo points to #%s, the token has Id %s", ref, tokenID)
	}

	wantData := `<v4:Data celk_trzba="250.00" dat_trzby="2027-01-08T21:19:38+01:00" eic_popl="CZ00000019" ` +
		`id_jednotky="181" id_pokl="RECEPCE" porad_cis="2027/000123"></v4:Data>`
	if !strings.Contains(s, wantData) {
		t.Fatalf("Data element does not match:\n%s", trzba)
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
