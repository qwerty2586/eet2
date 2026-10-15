package eet2

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

const soapWrap = `<?xml version="1.0" encoding="UTF-8"?><soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:eet="http://fs.gov.cz/eet/schema/v4"><soapenv:Header/><soapenv:Body>%s</soapenv:Body></soapenv:Envelope>`

var reSentUUID = regexp.MustCompile(`uuid_zpravy="([^"]+)"`)

// fakeEET je testovací server, který zkontroluje požadavek a vrátí odpověď
// vytvořenou funkcí reply (dostane UUID z požadavku).
func fakeEET(t *testing.T, reply func(uuid string) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("metoda %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "text/xml; charset=utf-8" {
			t.Errorf("Content-Type %q", ct)
		}
		if sa := r.Header.Get("SOAPAction"); sa != `"http://fs.gov.cz/eet/OdeslaniTrzby"` {
			t.Errorf("SOAPAction %q", sa)
		}
		if r.ProtoMajor != 1 || r.ProtoMinor != 1 {
			t.Errorf("protokol %s, chci HTTP/1.1", r.Proto)
		}
		body, _ := io.ReadAll(r.Body)
		m := reSentUUID.FindSubmatch(body)
		if m == nil {
			t.Error("požadavek neobsahuje uuid_zpravy")
			return
		}
		w.Header().Set("X-Global-Transaction-Id", "test-gtid-1")
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		fmt.Fprintf(w, soapWrap, reply(string(m[1])))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testDispatcher(t *testing.T, srv *httptest.Server, opts ...Option) *Dispatcher {
	t.Helper()
	cert, key := testCert(t, "CZ00000019")
	opts = append([]Option{WithHTTPClient(srv.Client())}, opts...)
	d, err := NewDispatcherWithSigner(Service(srv.URL), cert, key, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSendPaymentOK(t *testing.T) {
	srv := fakeEET(t, func(uuid string) string {
		return `<eet:Odpoved><eet:Hlavicka uuid_zpravy="` + uuid + `" dat_prij="2027-03-04T18:25:21+01:00"/>` +
			`<eet:Potvrzeni pok="987a6be5-6af5-44f3-b4fc-987654321000-ff" test="true"/>` +
			`<eet:Varovani kod_varov="1">EIC poplatnika v datove zprave se neshoduje s EIC v certifikatu</eet:Varovani>` +
			`</eet:Odpoved>`
	})
	resp, err := testDispatcher(t, srv).SendPayment(testReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Pok != "987a6be5-6af5-44f3-b4fc-987654321000-ff" || !resp.Test || resp.Overeno {
		t.Errorf("odpověď %+v", resp)
	}
	if len(resp.Varovani) != 1 || resp.Varovani[0].Kod != 1 ||
		resp.Varovani[0].Text != "EIC poplatnika v datove zprave se neshoduje s EIC v certifikatu" {
		t.Errorf("varování %+v", resp.Varovani)
	}
	if resp.GlobalTransactionID != "test-gtid-1" {
		t.Errorf("GlobalTransactionID %q", resp.GlobalTransactionID)
	}
	if want := time.Date(2027, 3, 4, 17, 25, 21, 0, time.UTC); !resp.DatPrij.Equal(want) {
		t.Errorf("DatPrij %v", resp.DatPrij)
	}
}

func TestSendPaymentUUIDMismatch(t *testing.T) {
	srv := fakeEET(t, func(string) string {
		return `<eet:Odpoved><eet:Hlavicka uuid_zpravy="123e4567-e89b-42d3-a456-426655440000" dat_prij="2027-03-04T18:25:21+01:00"/>` +
			`<eet:Potvrzeni pok="987a6be5-6af5-44f3-b4fc-987654321000-02"/></eet:Odpoved>`
	})
	_, err := testDispatcher(t, srv).SendPayment(testReceipt())
	var ue *UnexpectedResponseError
	if !errors.As(err, &ue) {
		t.Fatalf("chci UnexpectedResponseError, mám %v", err)
	}
}

func TestSendPaymentVerificationMode(t *testing.T) {
	srv := fakeEET(t, func(uuid string) string {
		return `<eet:Odpoved><eet:Hlavicka uuid_zpravy="` + uuid + `" dat_odmit="2027-03-04T18:25:21+01:00"/>` +
			`<eet:Chyba kod="0" test="true">Datovou zpravu evidovane trzby v overovacim modu se podarilo zpracovat</eet:Chyba>` +
			`</eet:Odpoved>`
	})
	r := testReceipt()
	r.Overeni = true
	resp, err := testDispatcher(t, srv).SendPayment(r)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Overeno || resp.Pok != "" {
		t.Errorf("odpověď %+v", resp)
	}
}

func TestSendPaymentErrors(t *testing.T) {
	cases := []struct {
		kod       int
		retryable bool
	}{
		{-1, true},
		{3, false},
		{4, false},
		{7, false},
	}
	for _, c := range cases {
		srv := fakeEET(t, func(string) string {
			return fmt.Sprintf(`<eet:Odpoved><eet:Hlavicka dat_odmit="2027-03-04T18:25:21+01:00"/>`+
				`<eet:Chyba kod="%d">Popis chyby</eet:Chyba></eet:Odpoved>`, c.kod)
		})
		_, err := testDispatcher(t, srv).SendPayment(testReceipt())
		var ee *Error
		if !errors.As(err, &ee) || ee.Kod != c.kod || ee.Text != "Popis chyby" {
			t.Errorf("kód %d: chci *Error, mám %v", c.kod, err)
			continue
		}
		if IsRetryable(err) != c.retryable {
			t.Errorf("kód %d: IsRetryable = %v", c.kod, !c.retryable)
		}
	}
}

func TestSendPaymentSOAPFault(t *testing.T) {
	srv := fakeEET(t, func(string) string {
		return `<soapenv:Fault><faultcode>soapenv:Server</faultcode><faultstring>chyba</faultstring></soapenv:Fault>`
	})
	_, err := testDispatcher(t, srv).SendPayment(testReceipt())
	var ue *UnexpectedResponseError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "SOAP Fault") {
		t.Fatalf("chci SOAP Fault, mám %v", err)
	}
}

func TestSendPaymentTimeoutIsRetryable(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // server neodpoví (např. zprávu vyhodnotil jako útok)
	}))
	defer srv.Close()
	defer close(block)

	d := testDispatcher(t, srv, WithTimeout(time.Millisecond)) // zvýší se na MinTimeout
	if d.timeout != MinTimeout {
		t.Fatalf("timeout %v, chci %v", d.timeout, MinTimeout)
	}
	start := time.Now()
	_, err := d.SendPayment(testReceipt())
	if err == nil || !IsRetryable(err) {
		t.Fatalf("chci opakovatelnou chybu, mám %v", err)
	}
	if el := time.Since(start); el < MinTimeout || el > MinTimeout+2*time.Second {
		t.Errorf("čekání %v", el)
	}
}

func TestSendPaymentValidationNotSent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("neplatná tržba se nemá odeslat")
	}))
	defer srv.Close()
	r := testReceipt()
	r.IdPokl = ""
	_, err := testDispatcher(t, srv).SendPayment(r)
	var ve *ValidationError
	if !errors.As(err, &ve) || IsRetryable(err) {
		t.Fatalf("chci neopakovatelnou ValidationError, mám %v", err)
	}
}

func TestNewDispatcherFromP12(t *testing.T) {
	cert, key := testCert(t, "CZ00000019")
	p12, err := pkcs12.Modern.Encode(key, cert, nil, "heslo")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcherFromP12(PlaygroundService, p12, "heslo")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Certificate().Equal(cert) {
		t.Fatal("načten jiný certifikát")
	}
	if _, err := NewDispatcherFromP12(PlaygroundService, p12, "spatne"); err == nil {
		t.Fatal("špatné heslo mělo selhat")
	}

	// Totéž ze souboru.
	path := filepath.Join(t.TempDir(), "test.p12")
	if err := os.WriteFile(path, p12, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDispatcher(PlaygroundService, path, "heslo"); err != nil {
		t.Fatal(err)
	}

	// Starší formát PKCS#12 (3DES) – takové soubory vydávala původní EET.
	legacy, err := pkcs12.LegacyDES.Encode(key, cert, nil, "heslo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDispatcherFromP12(PlaygroundService, legacy, "heslo"); err != nil {
		t.Fatal(err)
	}
}

func TestNewDispatcherFromPEM(t *testing.T) {
	cert, key := testCert(t, "CZ00000019")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der8, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der8})

	for name, keyPEM := range map[string][]byte{"PKCS#1": pkcs1, "PKCS#8": pkcs8} {
		if _, err := NewDispatcherFromPEM(PlaygroundService, certPEM, keyPEM); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Certifikát i klíč v jednom souboru.
	combined := append(append([]byte{}, certPEM...), pkcs8...)
	if _, err := NewDispatcherFromPEM(PlaygroundService, combined, nil); err != nil {
		t.Errorf("společný soubor: %v", err)
	}
	// Klíč, který k certifikátu nepatří.
	_, other := testCert(t, "CZ00000019")
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)})
	if _, err := NewDispatcherFromPEM(PlaygroundService, certPEM, otherPEM); err == nil {
		t.Error("cizí klíč měl selhat")
	}
}
