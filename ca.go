package eet2

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CAService is the address of the EET certificate authority API (CA EET v2),
// used to renew the cash register certificate automatically.
type CAService string

const (
	// CAProductionService is the production CA EET. A Dispatcher created
	// with ProductionService uses it automatically.
	CAProductionService CAService = "https://ca.caeet.gov.cz/api"

	// CATestService is the CA EET test environment.
	CATestService CAService = "https://ca.test.caeet.gov.cz/api"

	// CATrialService is the CA EET trial environment ("zkušební prostředí").
	CATrialService CAService = "https://ca.zkus.caeet.gov.cz/api"
)

// WithCAService sets the certificate authority API used by RenewCertificate.
// CAProductionService is used automatically for ProductionService.
func WithCAService(s CAService) Option {
	return func(d *Dispatcher) { d.ca = s }
}

// Certificate request states.
const (
	caStateInProcess  = "INPROCESS"
	caStateIssued     = "ISSUED"
	caStateDelivering = "DELIVERING"
	caStateFinished   = "FINISHED"
	caStateRejected   = "REJECTED"
)

const (
	// caRequestTimeout limits a single HTTP request to the CA (not the whole wait).
	caRequestTimeout = 30 * time.Second
	// caMaxPoll is the longest pause between status checks.
	caMaxPoll = 5 * time.Minute
	// jwtLifetime is the token lifetime; the CA allows at most 5 minutes from iat.
	jwtLifetime = 4 * time.Minute
)

// pollUnit is the unit of pollAfterSeconds (shortened in tests).
var pollUnit = time.Second

// CAError is an error reply from the certificate authority API.
type CAError struct {
	StatusCode     int
	Code           int
	Message        string
	AdditionalInfo string
	// RetryAfter is the suggested pause before the next attempt (HTTP 429).
	RetryAfter time.Duration
}

func (e *CAError) Error() string {
	s := fmt.Sprintf("eet2: CA EET HTTP %d", e.StatusCode)
	if e.Message != "" {
		s += fmt.Sprintf(": %s (code %d)", e.Message, e.Code)
	}
	if e.AdditionalInfo != "" {
		s += ": " + e.AdditionalInfo
	}
	return s
}

// Temporary is true for errors worth retrying later (rate limiting,
// server errors).
func (e *CAError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// RejectedError means the CA rejected the renewal request.
type RejectedError struct {
	ReqID  string
	Reason string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("eet2: CA EET rejected request %s: %s", e.ReqID, e.Reason)
}

// Renewal is an issued and downloaded new certificate whose receipt has not
// been confirmed yet. Store P12 and Password, then call Confirm.
//
// Until the receipt is confirmed, the certificate can be downloaded again:
// the next RenewCertificate call picks up the same request (for up to 30 days
// after issue).
type Renewal struct {
	// ReqID is the CA EET request identifier.
	ReqID string
	// P12 is the new certificate with its private key in PKCS#12 format.
	P12 []byte
	// Password opens P12.
	Password string
	// Certificate is the new certificate (NotBefore, NotAfter, SerialNumber…).
	Certificate *x509.Certificate

	d         *Dispatcher
	cred      *credentials
	confirmed bool
}

// String does not print P12 or the password, so they never end up in logs.
func (r *Renewal) String() string {
	return fmt.Sprintf("eet2.Renewal{ReqID: %s, Serial: %s, NotAfter: %s}",
		r.ReqID, r.Certificate.SerialNumber, r.Certificate.NotAfter.Format(time.RFC3339))
}

// GoString hides P12 and the password for %#v as well.
func (r *Renewal) GoString() string { return r.String() }

// RenewCertificate renews the cash register certificate through the CA EET
// API and returns the new certificate. The Dispatcher keeps using the old
// one: it switches only in Renewal.Confirm, which you call after storing P12
// and Password safely.
//
// If the CA has an unfinished request (e.g. the application crashed before
// Confirm), it is picked up instead of filing a new one. The call waits for
// the certificate to be issued; ctx limits how long. The request keeps going
// at the CA after ctx is cancelled and the next call picks it up.
//
// Renew while the current certificate is still valid. The tax administration
// recommends 2–3 weeks before it expires.
func (d *Dispatcher) RenewCertificate(ctx context.Context) (*Renewal, error) {
	if d.ca == "" {
		return nil, errors.New("eet2: no certificate authority API configured, use WithCAService")
	}

	reqID, state, err := d.findUnfinishedRequest(ctx)
	if err != nil {
		return nil, err
	}
	if reqID == "" {
		var res struct {
			ReqID string `json:"reqId"`
		}
		if err := d.caCall(ctx, http.MethodPost, "/request/renew", nil, &res, nil); err != nil {
			return nil, err
		}
		if res.ReqID == "" {
			return nil, errors.New("eet2: CA EET returned no reqId")
		}
		reqID, state = res.ReqID, caStateInProcess
	}

	for state == caStateInProcess {
		st, err := d.caStatus(ctx, reqID)
		if err != nil {
			return nil, err
		}
		state = st.State
		switch st.State {
		case caStateInProcess:
			if err := sleepCtx(ctx, st.wait); err != nil {
				return nil, err
			}
		case caStateIssued, caStateDelivering:
		case caStateRejected:
			return nil, &RejectedError{ReqID: reqID, Reason: st.RejectedReason}
		case caStateFinished:
			return nil, fmt.Errorf("eet2: request %s is already finished, the certificate was taken earlier", reqID)
		default:
			return nil, fmt.Errorf("eet2: unknown state of request %s: %q", reqID, st.State)
		}
	}

	var dto struct {
		Pkcs12   string `json:"pkcs12"`
		Password string `json:"password"`
	}
	if err := d.caCall(ctx, http.MethodPost, "/request/"+reqID+"/claim-download", nil, &dto, nil); err != nil {
		return nil, err
	}
	p12, err := base64.StdEncoding.DecodeString(dto.Pkcs12)
	if err != nil {
		return nil, fmt.Errorf("eet2: PKCS#12 from CA EET is not valid Base64: %w", err)
	}
	cred, err := credentialsFromP12(p12, dto.Password)
	if err != nil {
		return nil, fmt.Errorf("eet2: cannot load the certificate from CA EET (receipt not confirmed): %w", err)
	}
	if old := d.Certificate(); cred.cert.Subject.CommonName != old.Subject.CommonName {
		return nil, fmt.Errorf("eet2: the new certificate is issued to %q, the current one to %q (receipt not confirmed)",
			cred.cert.Subject.CommonName, old.Subject.CommonName)
	}
	return &Renewal{ReqID: reqID, P12: p12, Password: dto.Password, Certificate: cred.cert, d: d, cred: cred}, nil
}

// Confirm confirms receipt of the certificate to CA EET and switches the
// Dispatcher to the new certificate. Call it only after P12 and Password
// are stored safely: once confirmed, the certificate cannot be downloaded
// again.
//
// Calling it again is safe.
func (r *Renewal) Confirm(ctx context.Context) error {
	if r.confirmed {
		return nil
	}
	path := "/request/" + r.ReqID + "/ack-download"
	err := r.d.caCall(ctx, http.MethodPost, path, nil, nil, nil)

	var ce *CAError
	if errors.As(err, &ce) && ce.StatusCode == http.StatusUnauthorized && r.cred != r.d.cred.Load() {
		// The old certificate may have expired meanwhile: sign in with the new one.
		err = r.d.caCall(ctx, http.MethodPost, path, nil, nil, r.cred)
	}
	if errors.As(err, &ce) && ce.StatusCode == http.StatusConflict {
		// The receipt may already be confirmed (e.g. the reply got lost).
		if st, serr := r.d.caStatus(ctx, r.ReqID); serr == nil && st.State == caStateFinished {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	r.d.cred.Store(r.cred)
	r.confirmed = true
	return nil
}

// findUnfinishedRequest returns an unfinished request if there is one,
// preferring one whose certificate is already issued.
func (d *Dispatcher) findUnfinishedRequest(ctx context.Context) (reqID, state string, err error) {
	var list struct {
		Data []string `json:"data"`
	}
	if err := d.caCall(ctx, http.MethodGet, "/request/not-finished", nil, &list, nil); err != nil {
		return "", "", err
	}
	for _, id := range list.Data {
		st, err := d.caStatus(ctx, id)
		if err != nil {
			return "", "", err
		}
		switch st.State {
		case caStateIssued, caStateDelivering:
			return id, st.State, nil
		case caStateInProcess:
			if reqID == "" {
				reqID, state = id, st.State
			}
		}
	}
	return reqID, state, nil
}

type caStatusResult struct {
	State            string `json:"state"`
	PollAfterSeconds int    `json:"pollAfterSeconds"`
	RejectedReason   string `json:"rejectedReason"`
	wait             time.Duration
}

func (d *Dispatcher) caStatus(ctx context.Context, reqID string) (*caStatusResult, error) {
	var st caStatusResult
	var hdr http.Header
	if err := d.caCall(ctx, http.MethodGet, "/request/"+reqID+"/status", nil, &st, nil, &hdr); err != nil {
		return nil, err
	}
	secs := st.PollAfterSeconds
	if ra, err := strconv.Atoi(hdr.Get("Retry-After")); err == nil && ra > secs {
		secs = ra
	}
	if secs < 1 {
		secs = 1
	}
	st.wait = time.Duration(secs) * pollUnit
	if st.wait > caMaxPoll {
		st.wait = caMaxPoll
	}
	return &st, nil
}

// caCall makes one request to the CA EET API. cred selects the certificate
// for the JWT (nil = the Dispatcher's current certificate). out may be nil.
func (d *Dispatcher) caCall(ctx context.Context, method, path string, in, out any, cred *credentials, hdr ...*http.Header) error {
	if cred == nil {
		cred = d.cred.Load()
	}
	token, err := caToken(cred, d.now())
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, caRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(string(d.ca), "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("eet2: CA EET: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("eet2: CA EET: reading response: %w", err)
	}
	if len(hdr) > 0 && hdr[0] != nil {
		*hdr[0] = resp.Header
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ce := &CAError{StatusCode: resp.StatusCode}
		var dto struct {
			Code           int    `json:"code"`
			Message        string `json:"message"`
			AdditionalInfo string `json:"additionalInfo"`
		}
		if json.Unmarshal(data, &dto) == nil {
			ce.Code, ce.Message, ce.AdditionalInfo = dto.Code, dto.Message, dto.AdditionalInfo
		}
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && ra > 0 {
			ce.RetryAfter = time.Duration(ra) * time.Second
		}
		return ce
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("eet2: CA EET: invalid response: %w", err)
		}
	}
	return nil
}

// caToken creates a JWT (RS256) signed with the cash register certificate.
// The certificate is identified by x5t#S256 = base64url(SHA-256(DER)).
func caToken(c *credentials, now time.Time) (string, error) {
	b64 := base64.RawURLEncoding
	thumb := sha256.Sum256(c.cert.Raw)
	header, _ := json.Marshal(map[string]string{
		"alg":      "RS256",
		"typ":      "JWT",
		"x5t#S256": b64.EncodeToString(thumb[:]),
	})
	iat := now.Unix()
	payload, _ := json.Marshal(map[string]int64{"iat": iat, "exp": iat + int64(jwtLifetime/time.Second)})
	signingInput := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	h := sha256.Sum256([]byte(signingInput))
	sig, err := c.key.Sign(rand.Reader, h[:], crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("eet2: signing JWT: %w", err)
	}
	return signingInput + "." + b64.EncodeToString(sig), nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
