package eet2

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"
)

func init() { pollUnit = 10 * time.Millisecond }

// fakeCA simuluje API CA EET podle caeetapi_jwt.yml.
type fakeCA struct {
	t      *testing.T
	mu     sync.Mutex
	certs  map[string]*x509.Certificate // x5t#S256 → certifikát, kterým se lze přihlásit
	reqs   map[string]*fakeReq
	p12    []byte
	pass   string
	calls  map[string]int
	polls  int  // kolikrát vrátit INPROCESS
	reject bool // zamítnout žádost
	// ackConflict: ack vrátí 409 a žádost přejde do FINISHED (ztracená odpověď)
	ackConflict bool
	tokenCerts  []string // x5t#S256 certifikátů, kterými byly podepsány tokeny
}

type fakeReq struct {
	state string
	polls int
}

func newFakeCA(t *testing.T, login *x509.Certificate, p12 []byte, pass string) (*fakeCA, *httptest.Server) {
	ca := &fakeCA{t: t, certs: map[string]*x509.Certificate{}, reqs: map[string]*fakeReq{},
		p12: p12, pass: pass, calls: map[string]int{}}
	ca.allow(login)
	srv := httptest.NewTLSServer(ca)
	t.Cleanup(srv.Close)
	return ca, srv
}

func (ca *fakeCA) allow(c *x509.Certificate) {
	h := sha256.Sum256(c.Raw)
	ca.certs[base64.RawURLEncoding.EncodeToString(h[:])] = c
}

func (ca *fakeCA) count(name string) int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.calls[name]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func caErr(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, map[string]any{"code": code, "message": msg})
}

// checkToken ověří JWT podle profilu bearerJwtRs256.
func (ca *fakeCA) checkToken(r *http.Request) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return errors.New("chybí Bearer token")
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
	if len(parts) != 3 {
		return errors.New("token nemá 3 části")
	}
	b64 := base64.RawURLEncoding
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return err
	}
	var hdr map[string]any
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return err
	}
	if hdr["alg"] != "RS256" {
		return fmt.Errorf("alg %v", hdr["alg"])
	}
	if _, ok := hdr["x5c"]; ok {
		return errors.New("x5c se nemá posílat")
	}
	thumb, _ := hdr["x5t#S256"].(string)
	cert, ok := ca.certs[thumb]
	if !ok {
		return errors.New("neznámý certifikát x5t#S256")
	}
	pb, err := b64.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct{ Iat, Exp int64 }
	if err := json.Unmarshal(pb, &claims); err != nil {
		return err
	}
	if claims.Exp <= claims.Iat || claims.Exp-claims.Iat > 300 {
		return fmt.Errorf("iat %d exp %d", claims.Iat, claims.Exp)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(cert.PublicKey.(*rsa.PublicKey), crypto.SHA256, h[:], sig); err != nil {
		return fmt.Errorf("podpis tokenu: %w", err)
	}
	ca.tokenCerts = append(ca.tokenCerts, thumb)
	return nil
}

func (ca *fakeCA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if err := ca.checkToken(r); err != nil {
		caErr(w, http.StatusUnauthorized, 2, err.Error())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api")
	switch {
	case r.Method == http.MethodGet && path == "/request/not-finished":
		ca.calls["not-finished"]++
		ids := []string{}
		for id, rq := range ca.reqs {
			if rq.state == caStateInProcess || rq.state == caStateIssued || rq.state == caStateDelivering {
				ids = append(ids, id)
			}
		}
		writeJSON(w, 200, map[string]any{"count": len(ids), "data": ids})
	case r.Method == http.MethodPost && path == "/request/renew":
		ca.calls["renew"]++
		id := NewUUID()
		ca.reqs[id] = &fakeReq{state: caStateInProcess, polls: ca.polls}
		writeJSON(w, 201, map[string]string{"reqId": id})
	case strings.HasPrefix(path, "/request/"):
		rest := strings.Split(strings.TrimPrefix(path, "/request/"), "/")
		rq, ok := ca.reqs[rest[0]]
		if !ok || len(rest) != 2 {
			caErr(w, 404, 4, "Unknown object.")
			return
		}
		switch {
		case r.Method == http.MethodGet && rest[1] == "status":
			ca.calls["status"]++
			if rq.state == caStateInProcess {
				if rq.polls > 0 {
					rq.polls--
					w.Header().Set("Retry-After", "1")
					writeJSON(w, 200, map[string]any{"reqId": rest[0], "state": rq.state, "pollAfterSeconds": 1})
					return
				}
				rq.state = caStateIssued
				if ca.reject {
					rq.state = caStateRejected
				}
			}
			resp := map[string]any{"reqId": rest[0], "state": rq.state}
			if rq.state == caStateRejected {
				resp["rejectedReason"] = "Selhání kontroly parametrů žádosti"
			}
			writeJSON(w, 200, resp)
		case r.Method == http.MethodPost && rest[1] == "claim-download":
			ca.calls["claim"]++
			if rq.state != caStateIssued && rq.state != caStateDelivering {
				caErr(w, 409, 5, "Conflict.")
				return
			}
			rq.state = caStateDelivering
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, 200, map[string]any{
				"pkcs12":   base64.StdEncoding.EncodeToString(ca.p12),
				"password": ca.pass,
				"certificate": map[string]any{
					"sn": "1", "validFrom": "2027-01-01T00:00:00Z", "validTo": "2028-01-01T00:00:00Z", "state": "VALID",
				},
			})
		case r.Method == http.MethodPost && rest[1] == "ack-download":
			ca.calls["ack"]++
			if rq.state != caStateDelivering {
				caErr(w, 409, 5, "Conflict.")
				return
			}
			rq.state = caStateFinished
			if ca.ackConflict {
				caErr(w, 409, 5, "Conflict.")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			caErr(w, 400, 1, "Invalid input data.")
		}
	default:
		caErr(w, 404, 4, "Unknown object.")
	}
}

// renewSetup připraví Dispatcher se starým certifikátem a CA, která vydá nový.
func renewSetup(t *testing.T) (d *Dispatcher, ca *fakeCA, oldCert, newCert *x509.Certificate) {
	t.Helper()
	oldCert, oldKey := testCert(t, "CZ00000019")
	newCert, newKey := testCert(t, "CZ00000019")
	p12, err := pkcs12.LegacyDES.Encode(newKey, newCert, nil, "ABcd1234") // CA EET vydává 3DES
	if err != nil {
		t.Fatal(err)
	}
	ca, srv := newFakeCA(t, oldCert, p12, "ABcd1234")
	d, err = NewDispatcherWithSigner(PlaygroundService, oldCert, oldKey,
		WithHTTPClient(srv.Client()), WithCAService(CAService(srv.URL+"/api")))
	if err != nil {
		t.Fatal(err)
	}
	return d, ca, oldCert, newCert
}

func TestRenewCertificateFullFlow(t *testing.T) {
	d, ca, oldCert, newCert := renewSetup(t)
	ca.polls = 2

	ren, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ren.Certificate.Equal(newCert) || ren.Heslo != "ABcd1234" || len(ren.P12) == 0 {
		t.Fatalf("Renewal %v", ren)
	}
	if !d.Certificate().Equal(oldCert) {
		t.Fatal("před Confirm se nemá přepnout certifikát")
	}
	if ca.count("ack") != 0 {
		t.Fatal("převzetí se nemá potvrdit před Confirm")
	}
	if ca.count("renew") != 1 || ca.count("status") != 3 || ca.count("claim") != 1 {
		t.Fatalf("volání %v", ca.calls)
	}

	if err := ren.Confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d.Certificate().Equal(newCert) {
		t.Fatal("po Confirm má Dispatcher používat nový certifikát")
	}
	if ca.count("ack") != 1 {
		t.Fatalf("ack %d×", ca.count("ack"))
	}
	if err := ren.Confirm(context.Background()); err != nil || ca.count("ack") != 1 {
		t.Fatalf("opakovaný Confirm: %v, ack %d×", err, ca.count("ack"))
	}

	// Tržby se teď podepisují novým certifikátem.
	x, _, err := d.BuildRequest(testReceipt())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(x), base64.StdEncoding.EncodeToString(newCert.Raw)) {
		t.Fatal("zpráva neobsahuje nový certifikát")
	}
	// Všechny tokeny podepsal starý certifikát.
	oldThumb := sha256.Sum256(oldCert.Raw)
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if len(ca.tokenCerts) == 0 {
		t.Fatal("žádný token")
	}
	for _, tc := range ca.tokenCerts {
		if tc != base64.RawURLEncoding.EncodeToString(oldThumb[:]) {
			t.Fatalf("token podepsán jiným certifikátem: %s", tc)
		}
	}
}

func TestRenewCertificateResumesAfterCrash(t *testing.T) {
	d, ca, oldCert, newCert := renewSetup(t)

	first, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Aplikace spadne před Confirm. Po restartu se zavolá znovu.
	second, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.ReqID != first.ReqID || !second.Certificate.Equal(newCert) {
		t.Fatalf("nenavázalo na žádost %s, má %s", first.ReqID, second.ReqID)
	}
	if ca.count("renew") != 1 {
		t.Fatalf("podána druhá žádost: renew %d×", ca.count("renew"))
	}
	if !d.Certificate().Equal(oldCert) {
		t.Fatal("bez Confirm se nemá přepnout certifikát")
	}
	if err := second.Confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRenewCertificateResumesInProcess(t *testing.T) {
	d, ca, _, _ := renewSetup(t)
	ca.reqs["3fa85f64-5717-4562-b3fc-2c963f66afa6"] = &fakeReq{state: caStateInProcess, polls: 1}
	ren, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ren.ReqID != "3fa85f64-5717-4562-b3fc-2c963f66afa6" || ca.count("renew") != 0 {
		t.Fatalf("nenavázalo na rozpracovanou žádost: %s, renew %d×", ren.ReqID, ca.count("renew"))
	}
}

func TestRenewCertificateRejected(t *testing.T) {
	d, ca, _, _ := renewSetup(t)
	ca.reject = true
	_, err := d.RenewCertificate(context.Background())
	var re *RejectedError
	if !errors.As(err, &re) || re.Reason != "Selhání kontroly parametrů žádosti" {
		t.Fatalf("chci RejectedError, mám %v", err)
	}
}

func TestRenewConfirmConflictButFinished(t *testing.T) {
	d, ca, _, newCert := renewSetup(t)
	ca.ackConflict = true
	ren, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := ren.Confirm(context.Background()); err != nil {
		t.Fatalf("409 při již potvrzeném převzetí má projít: %v", err)
	}
	if !d.Certificate().Equal(newCert) {
		t.Fatal("certifikát se nepřepnul")
	}
}

func TestRenewConfirmWithExpiredOldCert(t *testing.T) {
	d, ca, oldCert, newCert := renewSetup(t)
	ren, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Starý certifikát mezitím vypršel, CA ho už nepřijme; nový ano.
	h := sha256.Sum256(oldCert.Raw)
	delete(ca.certs, base64.RawURLEncoding.EncodeToString(h[:]))
	ca.allow(newCert)
	if err := ren.Confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !d.Certificate().Equal(newCert) {
		t.Fatal("certifikát se nepřepnul")
	}
}

func TestRenewBadP12NotAcknowledged(t *testing.T) {
	d, ca, _, _ := renewSetup(t)
	ca.pass = "Spatne12"
	if _, err := d.RenewCertificate(context.Background()); err == nil {
		t.Fatal("chybné heslo k PKCS#12 mělo selhat")
	}
	if ca.count("ack") != 0 {
		t.Fatal("nečitelný certifikát se nesmí potvrdit")
	}
}

func TestRenewWrongEICRejected(t *testing.T) {
	d, ca, _, _ := renewSetup(t)
	other, otherKey := testCert(t, "CZ8551015704")
	p12, _ := pkcs12.LegacyDES.Encode(otherKey, other, nil, "ABcd1234")
	ca.p12 = p12
	if _, err := d.RenewCertificate(context.Background()); err == nil || !strings.Contains(err.Error(), "CZ8551015704") {
		t.Fatalf("certifikát na jiné EIČ měl selhat, mám %v", err)
	}
}

func TestRenewContextCancel(t *testing.T) {
	d, ca, _, _ := renewSetup(t)
	ca.polls = 1000
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := d.RenewCertificate(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("chci DeadlineExceeded, mám %v", err)
	}
	// Žádost u CA zůstává a příští volání na ni naváže.
	ca.polls = 0
	for _, rq := range ca.reqs {
		rq.polls = 0
	}
	if _, err := d.RenewCertificate(context.Background()); err != nil || ca.count("renew") != 1 {
		t.Fatalf("navázání po zrušení: %v, renew %d×", err, ca.count("renew"))
	}
}

func TestRenewUnauthorized(t *testing.T) {
	d, ca, oldCert, _ := renewSetup(t)
	h := sha256.Sum256(oldCert.Raw)
	delete(ca.certs, base64.RawURLEncoding.EncodeToString(h[:]))
	_, err := d.RenewCertificate(context.Background())
	var ce *CAError
	if !errors.As(err, &ce) || ce.StatusCode != 401 || ce.Code != 2 || ce.Temporary() {
		t.Fatalf("chci CAError 401, mám %v", err)
	}
}

func TestRenewNeedsCAService(t *testing.T) {
	cert, key := testCert(t, "CZ00000019")
	d, _ := NewDispatcherWithSigner(PlaygroundService, cert, key)
	if _, err := d.RenewCertificate(context.Background()); err == nil {
		t.Fatal("bez CA služby má selhat")
	}
	p, _ := NewDispatcherWithSigner(ProductionService, cert, key)
	if p.ca != CAProductionService {
		t.Fatalf("produkce má výchozí CA %q", p.ca)
	}
}

func TestRenewalStringHidesSecrets(t *testing.T) {
	d, _, _, _ := renewSetup(t)
	ren, err := d.RenewCertificate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(ren), fmt.Sprintf("%v", ren), fmt.Sprintf("%#v", ren), fmt.Sprintf("%+v", ren)} {
		if strings.Contains(s, "ABcd1234") || strings.Contains(s, "P12:") {
			t.Fatalf("výpis prozrazuje tajné údaje: %s", s)
		}
	}
}

// Výměna certifikátu za běhu nesmí kolidovat se souběžným odesíláním (go test -race).
func TestUpdateCertificateConcurrent(t *testing.T) {
	certA, keyA := testCert(t, "CZ00000019")
	certB, keyB := testCert(t, "CZ00000019")
	d, err := NewDispatcherWithSigner(PlaygroundService, certA, keyA)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, _, err := d.BuildRequest(testReceipt()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		c, k := certA, keyA
		if i%2 == 0 {
			c, k = certB, keyB
		}
		if err := d.UpdateCertificate(c, k); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	if err := d.UpdateCertificate(certA, keyB); err == nil {
		t.Fatal("klíč nepatřící k certifikátu měl selhat")
	}
	p12, _ := pkcs12.Modern.Encode(keyB, certB, nil, "x")
	if err := d.UpdateCertificateFromP12(p12, "x"); err != nil || !d.Certificate().Equal(certB) {
		t.Fatalf("UpdateCertificateFromP12: %v", err)
	}
}
