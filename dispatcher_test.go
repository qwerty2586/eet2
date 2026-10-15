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

// fakeEET is a test server that checks the request and returns the reply
// built by reply (which gets the request's UUID).
func fakeEET(t *testing.T, reply func(uuid string) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "text/xml; charset=utf-8" {
			t.Errorf("Content-Type %q", ct)
		}
		if sa := r.Header.Get("SOAPAction"); sa != `"http://fs.gov.cz/eet/OdeslaniTrzby"` {
			t.Errorf("SOAPAction %q", sa)
		}
		if r.ProtoMajor != 1 || r.ProtoMinor != 1 {
			t.Errorf("protocol %s, want HTTP/1.1", r.Proto)
		}
		body, _ := io.ReadAll(r.Body)
		m := reSentUUID.FindSubmatch(body)
		if m == nil {
			t.Error("request has no uuid_zpravy")
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
	if resp.Pok != "987a6be5-6af5-44f3-b4fc-987654321000-ff" || !resp.Test || resp.Verified {
		t.Errorf("response %+v", resp)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0].Code != 1 ||
		resp.Warnings[0].Text != "EIC poplatnika v datove zprave se neshoduje s EIC v certifikatu" {
		t.Errorf("warnings %+v", resp.Warnings)
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
		t.Fatalf("want UnexpectedResponseError, got %v", err)
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
	if !resp.Verified || resp.Pok != "" {
		t.Errorf("response %+v", resp)
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
				`<eet:Chyba kod="%d">Error text</eet:Chyba></eet:Odpoved>`, c.kod)
		})
		_, err := testDispatcher(t, srv).SendPayment(testReceipt())
		var ee *Error
		if !errors.As(err, &ee) || ee.Code != c.kod || ee.Text != "Error text" {
			t.Errorf("code %d: want *Error, got %v", c.kod, err)
			continue
		}
		if IsRetryable(err) != c.retryable {
			t.Errorf("code %d: IsRetryable = %v", c.kod, !c.retryable)
		}
	}
}

func TestSendPaymentSOAPFault(t *testing.T) {
	srv := fakeEET(t, func(string) string {
		return `<soapenv:Fault><faultcode>soapenv:Server</faultcode><faultstring>failure</faultstring></soapenv:Fault>`
	})
	_, err := testDispatcher(t, srv).SendPayment(testReceipt())
	var ue *UnexpectedResponseError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "SOAP Fault") {
		t.Fatalf("want SOAP Fault, got %v", err)
	}
}

func TestSendPaymentTimeoutIsRetryable(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block // the server never replies (e.g. it took the message for an attack)
	}))
	defer srv.Close()
	defer close(block)

	d := testDispatcher(t, srv, WithTimeout(time.Millisecond)) // raised to MinTimeout
	if d.timeout != MinTimeout {
		t.Fatalf("timeout %v, want %v", d.timeout, MinTimeout)
	}
	start := time.Now()
	_, err := d.SendPayment(testReceipt())
	if err == nil || !IsRetryable(err) {
		t.Fatalf("want a retryable error, got %v", err)
	}
	if el := time.Since(start); el < MinTimeout || el > MinTimeout+10*time.Second {
		t.Errorf("waited %v", el)
	}
}

func TestSendPaymentValidationNotSent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an invalid sale must not be sent")
	}))
	defer srv.Close()
	r := testReceipt()
	r.IdPokl = ""
	_, err := testDispatcher(t, srv).SendPayment(r)
	var ve *ValidationError
	if !errors.As(err, &ve) || IsRetryable(err) {
		t.Fatalf("want a non-retryable ValidationError, got %v", err)
	}
}

func TestNewDispatcherFromP12(t *testing.T) {
	cert, key := testCert(t, "CZ00000019")
	p12, err := pkcs12.Modern.Encode(key, cert, nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDispatcherFromP12(PlaygroundService, p12, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Certificate().Equal(cert) {
		t.Fatal("loaded a different certificate")
	}
	if _, err := NewDispatcherFromP12(PlaygroundService, p12, "wrong"); err == nil {
		t.Fatal("a wrong password should fail")
	}

	// The same from a file.
	path := filepath.Join(t.TempDir(), "test.p12")
	if err := os.WriteFile(path, p12, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDispatcher(PlaygroundService, path, "secret"); err != nil {
		t.Fatal(err)
	}

	// Legacy PKCS#12 (3DES), as issued by CA EET.
	legacy, err := pkcs12.LegacyDES.Encode(key, cert, nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDispatcherFromP12(PlaygroundService, legacy, "secret"); err != nil {
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
	// Certificate and key in one file.
	combined := append(append([]byte{}, certPEM...), pkcs8...)
	if _, err := NewDispatcherFromPEM(PlaygroundService, combined, nil); err != nil {
		t.Errorf("combined file: %v", err)
	}
	// A key that does not match the certificate.
	_, other := testCert(t, "CZ00000019")
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)})
	if _, err := NewDispatcherFromPEM(PlaygroundService, certPEM, otherPEM); err == nil {
		t.Error("a foreign key should fail")
	}
}

func TestCheckP12(t *testing.T) {
	cert, key := testCert(t, "CZ00000019")
	for name, enc := range map[string]*pkcs12.Encoder{"modern": pkcs12.Modern, "legacy 3DES": pkcs12.LegacyDES} {
		p12, err := enc.Encode(key, cert, nil, "ABcd1234")
		if err != nil {
			t.Fatal(err)
		}

		got, err := CheckP12(p12, "ABcd1234")
		if err != nil || !got.Equal(cert) || got.Subject.CommonName != "CZ00000019" {
			t.Errorf("%s: correct password: %v", name, err)
		}

		_, err = CheckP12(p12, "wrong")
		if !errors.Is(err, ErrIncorrectPassword) {
			t.Errorf("%s: want ErrIncorrectPassword, got %v", name, err)
		}
		// The same error comes out of the Dispatcher constructors.
		if _, err := NewDispatcherFromP12(PlaygroundService, p12, "wrong"); !errors.Is(err, ErrIncorrectPassword) {
			t.Errorf("%s: NewDispatcherFromP12: want ErrIncorrectPassword, got %v", name, err)
		}
	}

	// A corrupt file is an error, but not a wrong password.
	if _, err := CheckP12([]byte("not a p12 file"), "ABcd1234"); err == nil || errors.Is(err, ErrIncorrectPassword) {
		t.Errorf("corrupt file: want a non-password error, got %v", err)
	}
}
