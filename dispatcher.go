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

// Service is the address of the EET web service.
type Service string

const (
	// PlaygroundService is the non-production environment for developers.
	// The returned POK ends with "-ff" and is not valid.
	PlaygroundService Service = "https://pg.trzbyeet.gov.cz/eet/services/EETServiceSOAP/v4"

	// ProductionService is the production environment.
	ProductionService Service = "https://trzbyeet.gov.cz/eet/services/EETServiceSOAP/v4"
)

const (
	// MinTimeout is the shortest response time limit the law allows.
	MinTimeout = 2 * time.Second
	// DefaultTimeout is the default response time limit.
	DefaultTimeout = 3 * time.Second
)

// Dispatcher sends sales to EET. It is safe for concurrent use, including
// swapping the certificate at runtime (UpdateCertificate, Renewal.Confirm).
type Dispatcher struct {
	service Service
	ca      CAService
	cred    atomic.Pointer[credentials]
	client  *http.Client
	timeout time.Duration
	now     func() time.Time
}

// credentials is the cash register certificate with its key. Always replaced as a whole.
type credentials struct {
	cert *x509.Certificate
	key  crypto.Signer
}

func newCredentials(cert *x509.Certificate, key crypto.Signer) (*credentials, error) {
	if cert == nil || key == nil {
		return nil, errors.New("eet2: missing certificate or key")
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("eet2: the certificate must have an RSA key")
	}
	if kp, ok := key.Public().(*rsa.PublicKey); !ok || !kp.Equal(pub) {
		return nil, errors.New("eet2: the private key does not match the certificate")
	}
	return &credentials{cert: cert, key: key}, nil
}

// ErrIncorrectPassword is returned (wrapped) when a PKCS#12 password is wrong.
// Check it with errors.Is(err, eet2.ErrIncorrectPassword).
var ErrIncorrectPassword = pkcs12.ErrIncorrectPassword

// CheckP12 checks a PKCS#12 certificate without creating a Dispatcher: that
// the password is correct, that it contains an RSA key and that the key
// matches the certificate. It returns the certificate (EIČ in
// Subject.CommonName, validity in NotBefore/NotAfter). Nothing is sent
// anywhere.
//
// A wrong password gives an error matching errors.Is(err, ErrIncorrectPassword).
func CheckP12(p12 []byte, password string) (*x509.Certificate, error) {
	c, err := credentialsFromP12(p12, password)
	if err != nil {
		return nil, err
	}
	return c.cert, nil
}

func credentialsFromP12(p12 []byte, password string) (*credentials, error) {
	key, cert, _, err := pkcs12.DecodeChain(p12, password)
	if err != nil {
		return nil, fmt.Errorf("eet2: decoding PKCS#12: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("eet2: the private key in PKCS#12 cannot sign")
	}
	return newCredentials(cert, signer)
}

// Option configures a Dispatcher.
type Option func(*Dispatcher)

// WithTimeout sets the response time limit. Values below MinTimeout (2 s)
// are raised to MinTimeout.
func WithTimeout(t time.Duration) Option {
	return func(d *Dispatcher) {
		if t < MinTimeout {
			t = MinTimeout
		}
		d.timeout = t
	}
}

// WithHTTPClient sets a custom HTTP client (proxy, custom transport…).
// The client must verify the server's TLS certificate.
func WithHTTPClient(c *http.Client) Option {
	return func(d *Dispatcher) { d.client = c }
}

// NewDispatcher loads the cash register certificate from a PKCS#12 file (.p12/.pfx).
func NewDispatcher(service Service, p12File, password string, opts ...Option) (*Dispatcher, error) {
	data, err := os.ReadFile(p12File)
	if err != nil {
		return nil, fmt.Errorf("eet2: reading certificate: %w", err)
	}
	return NewDispatcherFromP12(service, data, password, opts...)
}

// NewDispatcherFromP12 loads the cash register certificate from PKCS#12
// (.p12/.pfx) content in memory, e.g. from a database or a secrets vault.
func NewDispatcherFromP12(service Service, p12 []byte, password string, opts ...Option) (*Dispatcher, error) {
	c, err := credentialsFromP12(p12, password)
	if err != nil {
		return nil, err
	}
	return newDispatcher(service, c, opts)
}

// NewDispatcherFromPEM loads the certificate and private key in PEM format.
// The key may be PKCS#1 ("RSA PRIVATE KEY") or PKCS#8 ("PRIVATE KEY").
// certPEM and keyPEM may also be one file containing both blocks.
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
						return nil, fmt.Errorf("eet2: certificate: %w", err)
					}
					cert = c
				}
			case "RSA PRIVATE KEY":
				k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
				if err != nil {
					return nil, fmt.Errorf("eet2: private key: %w", err)
				}
				key = k
			case "PRIVATE KEY":
				k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
				if err != nil {
					return nil, fmt.Errorf("eet2: private key: %w", err)
				}
				s, ok := k.(crypto.Signer)
				if !ok {
					return nil, errors.New("eet2: the private key cannot sign")
				}
				key = s
			}
		}
	}
	if cert == nil {
		return nil, errors.New("eet2: PEM has no CERTIFICATE block")
	}
	if key == nil {
		return nil, errors.New("eet2: PEM has no private key")
	}
	return NewDispatcherWithSigner(service, cert, key, opts...)
}

// NewDispatcherWithSigner uses an already loaded certificate and key. Useful
// for keys in an HSM or cloud KMS that implement crypto.Signer.
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
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{} // HTTP/1.1 is mandatory
		d.client = &http.Client{Transport: tr}
	}
	return d, nil
}

// Certificate returns the current cash register certificate (e.g. to check NotAfter).
func (d *Dispatcher) Certificate() *x509.Certificate { return d.cred.Load().cert }

// UpdateCertificate swaps the certificate at runtime. Sends in progress
// finish with the old certificate, later ones use the new one.
func (d *Dispatcher) UpdateCertificate(cert *x509.Certificate, key crypto.Signer) error {
	c, err := newCredentials(cert, key)
	if err != nil {
		return err
	}
	d.cred.Store(c)
	return nil
}

// UpdateCertificateFromP12 swaps the certificate at runtime for one from
// PKCS#12 content, e.g. freshly downloaded from DIS+.
func (d *Dispatcher) UpdateCertificateFromP12(p12 []byte, password string) error {
	c, err := credentialsFromP12(p12, password)
	if err != nil {
		return err
	}
	d.cred.Store(c)
	return nil
}

// BuildRequest builds and signs the data message without sending it.
// It returns the XML and the message UUID. Useful for debugging or a
// custom transport.
func (d *Dispatcher) BuildRequest(r Receipt) (xml []byte, uuid string, err error) {
	header, data, err := r.attrs(d.now())
	if err != nil {
		return nil, "", err
	}
	c := d.cred.Load()
	xml, err = buildEnvelope(header, data, c.cert, c.key)
	if err != nil {
		return nil, "", err
	}
	return xml, header[0].value, nil
}

// SendPayment sends the sale and returns the reply with the POK.
//
// On error, check IsRetryable(err): if true, store the sale and send it
// again with the same data and Opakovane: true (within 48 hours at the latest).
func (d *Dispatcher) SendPayment(r Receipt) (*Response, error) {
	return d.SendPaymentContext(context.Background(), r)
}

// SendPaymentContext is SendPayment with a context. The response time
// limit (WithTimeout) always applies, even if the context has a later deadline.
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
		return nil, fmt.Errorf("eet2: sending: %w", err)
	}
	defer resp.Body.Close()
	gtid := resp.Header.Get("X-Global-Transaction-Id")
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("eet2: reading response: %w", err)
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil, &UnexpectedResponseError{resp.StatusCode, "empty response", gtid}
	}
	return parseResponse(respBody, resp.StatusCode, uuid, gtid)
}
