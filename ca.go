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

// CAService je adresa API certifikační autority EET (CA EET v2),
// přes které se automaticky obnovuje pokladní certifikát.
type CAService string

const (
	// CAProductionService je produkční CA EET. Dispatcher s ProductionService
	// ji používá automaticky.
	CAProductionService CAService = "https://ca.caeet.gov.cz/api"

	// CATestService je testovací prostředí CA EET.
	CATestService CAService = "https://ca.test.caeet.gov.cz/api"

	// CAZkusebniService je zkušební prostředí CA EET.
	CAZkusebniService CAService = "https://ca.zkus.caeet.gov.cz/api"
)

// WithCAService nastaví API certifikační autority pro RenewCertificate.
// Pro ProductionService se použije CAProductionService automaticky.
func WithCAService(s CAService) Option {
	return func(d *Dispatcher) { d.ca = s }
}

// Stavy žádosti o certifikát.
const (
	caStateInProcess  = "INPROCESS"
	caStateIssued     = "ISSUED"
	caStateDelivering = "DELIVERING"
	caStateFinished   = "FINISHED"
	caStateRejected   = "REJECTED"
)

const (
	// caRequestTimeout omezuje jeden HTTP požadavek na CA (ne celé čekání).
	caRequestTimeout = 30 * time.Second
	// caMaxPoll je nejdelší pauza mezi dotazy na stav žádosti.
	caMaxPoll = 5 * time.Minute
	// jwtLifetime je platnost tokenu; CA připouští nejvýše 5 minut od iat.
	jwtLifetime = 4 * time.Minute
)

// pollUnit je jednotka pro pollAfterSeconds (v testech se zkracuje).
var pollUnit = time.Second

// CAError je chybová odpověď API certifikační autority.
type CAError struct {
	StatusCode     int
	Code           int
	Message        string
	AdditionalInfo string
	// RetryAfter je doporučená pauza před dalším pokusem (u HTTP 429).
	RetryAfter time.Duration
}

func (e *CAError) Error() string {
	s := fmt.Sprintf("eet2: CA EET HTTP %d", e.StatusCode)
	if e.Message != "" {
		s += fmt.Sprintf(": %s (kód %d)", e.Message, e.Code)
	}
	if e.AdditionalInfo != "" {
		s += ": " + e.AdditionalInfo
	}
	return s
}

// Temporary je true pro chyby, po kterých má smysl to zkusit později
// (přetížení, chyba serveru).
func (e *CAError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// RejectedError znamená, že CA žádost o obnovu zamítla.
type RejectedError struct {
	ReqID  string
	Reason string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("eet2: CA EET zamítla žádost %s: %s", e.ReqID, e.Reason)
}

// Renewal je vydaný a stažený nový pokladní certifikát, jehož převzetí ještě
// nebylo potvrzeno. Uložte P12 a Heslo a pak zavolejte Confirm.
//
// Dokud není převzetí potvrzeno, lze certifikát znovu stáhnout: další volání
// RenewCertificate naváže na tutéž žádost (nejdéle 30 dní od vydání).
type Renewal struct {
	// ReqID je identifikátor žádosti u CA EET.
	ReqID string
	// P12 je nový certifikát s privátním klíčem ve formátu PKCS#12.
	P12 []byte
	// Heslo otevírá P12.
	Heslo string
	// Certificate je nový certifikát (NotBefore, NotAfter, SerialNumber…).
	Certificate *x509.Certificate

	d         *Dispatcher
	cred      *credentials
	confirmed bool
}

// String nevypisuje P12 ani heslo, aby se neocitly v logu.
func (r *Renewal) String() string {
	return fmt.Sprintf("eet2.Renewal{ReqID: %s, Serial: %s, NotAfter: %s}",
		r.ReqID, r.Certificate.SerialNumber, r.Certificate.NotAfter.Format(time.RFC3339))
}

// GoString nevypisuje P12 ani heslo ani při formátování %#v.
func (r *Renewal) GoString() string { return r.String() }

// RenewCertificate obnoví pokladní certifikát přes API CA EET a vrátí nový
// certifikát. Dispatcher zatím dál používá starý: na nový přepne až
// Renewal.Confirm, které zavolejte po bezpečném uložení P12 a hesla.
//
// Pokud u CA existuje rozpracovaná žádost (např. po pádu aplikace před
// Confirm), naváže na ni místo podání nové. Čeká na vydání certifikátu;
// ctx určuje, jak dlouho. Žádost u CA běží dál i po zrušení ctx a příští
// volání na ni naváže.
//
// Obnovovat je nutné, dokud je stávající certifikát platný. Finanční správa
// doporučuje 2–3 týdny před koncem platnosti.
func (d *Dispatcher) RenewCertificate(ctx context.Context) (*Renewal, error) {
	if d.ca == "" {
		return nil, errors.New("eet2: není nastaveno API certifikační autority, použijte WithCAService")
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
			return nil, errors.New("eet2: CA EET nevrátila reqId")
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
			return nil, fmt.Errorf("eet2: žádost %s je už ukončená, certifikát byl převzat dříve", reqID)
		default:
			return nil, fmt.Errorf("eet2: neznámý stav žádosti %s: %q", reqID, st.State)
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
		return nil, fmt.Errorf("eet2: PKCS#12 od CA EET není platné Base64: %w", err)
	}
	cred, err := credentialsFromP12(p12, dto.Password)
	if err != nil {
		return nil, fmt.Errorf("eet2: certifikát od CA EET nelze načíst (převzetí nebylo potvrzeno): %w", err)
	}
	if old := d.Certificate(); cred.cert.Subject.CommonName != old.Subject.CommonName {
		return nil, fmt.Errorf("eet2: nový certifikát je vydán na %q, stávající na %q (převzetí nebylo potvrzeno)",
			cred.cert.Subject.CommonName, old.Subject.CommonName)
	}
	return &Renewal{ReqID: reqID, P12: p12, Heslo: dto.Password, Certificate: cred.cert, d: d, cred: cred}, nil
}

// Confirm potvrdí CA EET převzetí certifikátu a přepne Dispatcher na nový
// certifikát. Volejte ho až po bezpečném uložení P12 a hesla: po potvrzení
// už certifikát nelze znovu stáhnout.
//
// Opakované volání je bezpečné.
func (r *Renewal) Confirm(ctx context.Context) error {
	if r.confirmed {
		return nil
	}
	path := "/request/" + r.ReqID + "/ack-download"
	err := r.d.caCall(ctx, http.MethodPost, path, nil, nil, nil)

	var ce *CAError
	if errors.As(err, &ce) && ce.StatusCode == http.StatusUnauthorized && r.cred != r.d.cred.Load() {
		// Starý certifikát mezitím mohl vypršet: zkusit se přihlásit novým.
		err = r.d.caCall(ctx, http.MethodPost, path, nil, nil, r.cred)
	}
	if errors.As(err, &ce) && ce.StatusCode == http.StatusConflict {
		// Převzetí už mohlo být potvrzeno (např. odpověď se ztratila po cestě).
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

// findUnfinishedRequest vrátí rozpracovanou žádost, pokud existuje.
// Přednost má žádost s vydaným certifikátem.
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

// caCall provede jeden požadavek na API CA EET. cred určuje certifikát pro
// JWT (nil = aktuální certifikát Dispatcheru). out může být nil.
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
		return fmt.Errorf("eet2: CA EET: čtení odpovědi: %w", err)
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
			return fmt.Errorf("eet2: CA EET: neplatná odpověď: %w", err)
		}
	}
	return nil
}

// caToken vytvoří JWT (RS256) podepsaný pokladním certifikátem. Certifikát
// se identifikuje otiskem x5t#S256 = base64url(SHA-256(DER)).
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
		return "", fmt.Errorf("eet2: podpis JWT: %w", err)
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
