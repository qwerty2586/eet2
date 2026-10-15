package eet2

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

// Service je adresa webové služby EET.
type Service string

const (
	// PlaygroundService je neprodukční prostředí pro vývojáře.
	// Vrácený POK končí na "-ff" a není platný.
	PlaygroundService Service = "https://pg.trzbyeet.gov.cz/eet/services/EETServiceSOAP/v4"

	// ProductionService je produkční prostředí.
	ProductionService Service = "https://trzbyeet.gov.cz/eet/services/EETServiceSOAP/v4"
)

const (
	// MinTimeout je nejkratší mezní doba odezvy, kterou zákon připouští.
	MinTimeout = 2 * time.Second
	// DefaultTimeout je výchozí mezní doba odezvy.
	DefaultTimeout = 3 * time.Second
)

// Dispatcher odesílá tržby do systému EET. Je bezpečný pro souběžné použití,
// včetně výměny certifikátu za běhu (UpdateCertificate, Renewal.Confirm).
type Dispatcher struct {
	service Service
	ca      CAService
	cred    atomic.Pointer[credentials]
	client  *http.Client
	timeout time.Duration
	now     func() time.Time
}

// credentials je pokladní certifikát s klíčem. Mění se vždy celé najednou.
type credentials struct {
	cert *x509.Certificate
	key  crypto.Signer
}

func newCredentials(cert *x509.Certificate, key crypto.Signer) (*credentials, error) {
	if cert == nil || key == nil {
		return nil, errors.New("eet2: chybí certifikát nebo klíč")
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("eet2: pokladní certifikát musí mít RSA klíč")
	}
	if kp, ok := key.Public().(*rsa.PublicKey); !ok || !kp.Equal(pub) {
		return nil, errors.New("eet2: privátní klíč nepatří k certifikátu")
	}
	return &credentials{cert: cert, key: key}, nil
}

func credentialsFromP12(p12 []byte, password string) (*credentials, error) {
	key, cert, _, err := pkcs12.DecodeChain(p12, password)
	if err != nil {
		return nil, fmt.Errorf("eet2: dekódování PKCS#12: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("eet2: privátní klíč v PKCS#12 nelze použít k podpisu")
	}
	return newCredentials(cert, signer)
}

// Option upravuje nastavení Dispatcheru.
type Option func(*Dispatcher)

// WithTimeout nastaví mezní dobu odezvy. Hodnoty pod MinTimeout (2 s)
// se zvýší na MinTimeout.
func WithTimeout(t time.Duration) Option {
	return func(d *Dispatcher) {
		if t < MinTimeout {
			t = MinTimeout
		}
		d.timeout = t
	}
}

// WithHTTPClient nastaví vlastního HTTP klienta (proxy, vlastní transport…).
// Klient musí ověřovat TLS certifikát serveru.
func WithHTTPClient(c *http.Client) Option {
	return func(d *Dispatcher) { d.client = c }
}

// NewDispatcher načte pokladní certifikát ze souboru PKCS#12 (.p12/.pfx).
func NewDispatcher(service Service, p12File, password string, opts ...Option) (*Dispatcher, error) {
	data, err := os.ReadFile(p12File)
	if err != nil {
		return nil, fmt.Errorf("eet2: čtení certifikátu: %w", err)
	}
	return NewDispatcherFromP12(service, data, password, opts...)
}

// NewDispatcherFromP12 načte pokladní certifikát z obsahu souboru PKCS#12
// (.p12/.pfx) v paměti, např. z databáze nebo trezoru tajemství.
func NewDispatcherFromP12(service Service, p12 []byte, password string, opts ...Option) (*Dispatcher, error) {
	c, err := credentialsFromP12(p12, password)
	if err != nil {
		return nil, err
	}
	return newDispatcher(service, c, opts)
}

// NewDispatcherFromPEM načte certifikát a privátní klíč ve formátu PEM.
// Klíč může být PKCS#1 ("RSA PRIVATE KEY") nebo PKCS#8 ("PRIVATE KEY").
// certPEM i keyPEM mohou být i jeden společný soubor s oběma bloky.
func NewDispatcherFromPEM(service Service, certPEM, keyPEM []byte, opts ...Option) (*Dispatcher, error) {
	var cert *x509.Certificate
	var key crypto.Signer
	for _, src := range [][]byte{certPEM, keyPEM} {
		rest := src
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			switch block.Type {
			case "CERTIFICATE":
				if cert == nil {
					c, err := x509.ParseCertificate(block.Bytes)
					if err != nil {
						return nil, fmt.Errorf("eet2: certifikát: %w", err)
					}
					cert = c
				}
			case "RSA PRIVATE KEY":
				k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
				if err != nil {
					return nil, fmt.Errorf("eet2: privátní klíč: %w", err)
				}
				key = k
			case "PRIVATE KEY":
				k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
				if err != nil {
					return nil, fmt.Errorf("eet2: privátní klíč: %w", err)
				}
				s, ok := k.(crypto.Signer)
				if !ok {
					return nil, errors.New("eet2: privátní klíč nelze použít k podpisu")
				}
				key = s
			}
		}
	}
	if cert == nil {
		return nil, errors.New("eet2: v PEM chybí blok CERTIFICATE")
	}
	if key == nil {
		return nil, errors.New("eet2: v PEM chybí privátní klíč")
	}
	return NewDispatcherWithSigner(service, cert, key, opts...)
}

// NewDispatcherWithSigner použije již načtený certifikát a klíč. Hodí se pro
// klíče v HSM nebo cloudovém KMS, které implementují crypto.Signer.
func NewDispatcherWithSigner(service Service, cert *x509.Certificate, key crypto.Signer, opts ...Option) (*Dispatcher, error) {
	c, err := newCredentials(cert, key)
	if err != nil {
		return nil, err
	}
	return newDispatcher(service, c, opts)
}

func newDispatcher(service Service, c *credentials, opts []Option) (*Dispatcher, error) {
	d := &Dispatcher{
		service: service,
		timeout: DefaultTimeout,
		now:     time.Now,
	}
	d.cred.Store(c)
	for _, o := range opts {
		o(d)
	}
	if d.ca == "" && service == ProductionService {
		d.ca = CAProductionService
	}
	if d.client == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{} // HTTP/1.1 je povinné
		d.client = &http.Client{Transport: tr}
	}
	return d, nil
}

// Certificate vrátí aktuální pokladní certifikát (např. pro kontrolu NotAfter).
func (d *Dispatcher) Certificate() *x509.Certificate { return d.cred.Load().cert }

// UpdateCertificate vymění pokladní certifikát za běhu. Probíhající odeslání
// doběhnou se starým certifikátem, další už použijí nový.
func (d *Dispatcher) UpdateCertificate(cert *x509.Certificate, key crypto.Signer) error {
	c, err := newCredentials(cert, key)
	if err != nil {
		return err
	}
	d.cred.Store(c)
	return nil
}

// UpdateCertificateFromP12 vymění pokladní certifikát za běhu za certifikát
// z obsahu souboru PKCS#12, např. nově stažený z DIS+.
func (d *Dispatcher) UpdateCertificateFromP12(p12 []byte, password string) error {
	c, err := credentialsFromP12(p12, password)
	if err != nil {
		return err
	}
	d.cred.Store(c)
	return nil
}

// BuildRequest sestaví a podepíše datovou zprávu bez odeslání. Vrací XML
// a UUID zprávy. Hodí se pro ladění nebo vlastní transport.
func (d *Dispatcher) BuildRequest(r Receipt) (xml []byte, uuid string, err error) {
	hlavicka, data, err := r.attrs(d.now())
	if err != nil {
		return nil, "", err
	}
	c := d.cred.Load()
	xml, err = buildEnvelope(hlavicka, data, c.cert, c.key)
	if err != nil {
		return nil, "", err
	}
	return xml, hlavicka[0].value, nil
}

// SendPayment odešle tržbu a vrátí odpověď s POK.
//
// Při chybě rozhodněte podle IsRetryable(err): pokud vrátí true, uložte
// tržbu a odešlete ji znovu se stejnými daty a Opakovane: true
// (nejpozději do 48 hodin).
func (d *Dispatcher) SendPayment(r Receipt) (*Response, error) {
	return d.SendPaymentContext(context.Background(), r)
}

// SendPaymentContext je SendPayment s kontextem. Mezní doba odezvy
// (WithTimeout) platí vždy, i když má kontext delší deadline.
func (d *Dispatcher) SendPaymentContext(ctx context.Context, r Receipt) (*Response, error) {
	body, uuid, err := d.BuildRequest(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, string(d.service), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"`+soapAction+`"`)

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eet2: odeslání: %w", err)
	}
	defer resp.Body.Close()
	gtid := resp.Header.Get("X-Global-Transaction-Id")
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("eet2: čtení odpovědi: %w", err)
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil, &UnexpectedResponseError{resp.StatusCode, "prázdná odpověď", gtid}
	}
	return parseResponse(respBody, resp.StatusCode, uuid, gtid)
}
