package eet2

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Response je úspěšná odpověď systému EET.
type Response struct {
	// UuidZpravy je UUID odeslané datové zprávy.
	UuidZpravy string

	// DatPrij je okamžik přijetí zprávy serverem.
	DatPrij time.Time

	// Pok je potvrzovací kód (39 znaků). V ověřovacím módu je prázdný.
	// V neprodukčním prostředí končí na "-ff" a není platný.
	Pok string

	// Test je true, pokud odpověď přišla z neprodukčního prostředí (playground).
	Test bool

	// Overeno je true, pokud šlo o ověřovací mód a zpráva prošla kontrolami
	// (server vrátil chybu s kódem 0). Tržba v tom případě NENÍ zaevidována.
	Overeno bool

	// Varovani jsou propustné chyby. POK je platný i s varováním,
	// ale data stojí za opravu.
	Varovani []Varovani

	// GlobalTransactionID je hodnota hlavičky X-Global-Transaction-Id
	// pro dohledání problémů s podporou finanční správy.
	GlobalTransactionID string
}

// Varovani je propustná chyba z odpovědi.
type Varovani struct {
	Kod  int
	Text string
}

// Error je chybová odpověď systému EET (kritická chyba nebo dočasná
// technická chyba na straně správce daně).
type Error struct {
	Kod                 int
	Text                string
	Test                bool
	UuidZpravy          string
	DatOdmit            time.Time
	GlobalTransactionID string
}

func (e *Error) Error() string {
	return fmt.Sprintf("eet2: chyba EET %d: %s", e.Kod, e.Text)
}

// Temporary je true pro dočasnou technickou chybu serveru (záporný kód).
// Zprávu je pak třeba odeslat znovu později.
func (e *Error) Temporary() bool { return e.Kod < 0 }

// Kódy chyb podle specifikace, kap. 3.5.4.
const (
	ErrKodDocasnaChyba   = -1 // dočasná technická chyba, odeslat později
	ErrKodOvereniOK      = 0  // ověřovací mód: zprávu se podařilo zpracovat
	ErrKodKodovani       = 2  // kódování XML není platné
	ErrKodSchema         = 3  // zpráva nevyhověla XML schématu
	ErrKodPodpis         = 4  // neplatný podpis SOAP zprávy
	ErrKodEIC            = 6  // EIČ poplatníka má chybnou strukturu
	ErrKodVelikost       = 7  // zpráva je příliš velká
	ErrKodTechnickaChyba = 8  // technická chyba nebo chyba dat
)

// IsRetryable vrátí true, pokud má smysl tutéž tržbu odeslat znovu
// (s Opakovane: true): výpadek spojení, timeout nebo dočasná chyba serveru.
// Pro chyby validace a kritické chyby zprávy vrací false.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var ve *ValidationError
	if errors.As(err, &ve) {
		return false
	}
	var ee *Error
	if errors.As(err, &ee) {
		return ee.Temporary()
	}
	var ue *UnexpectedResponseError
	if errors.As(err, &ue) {
		return ue.StatusCode >= 500 || ue.StatusCode == 0
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	// Ostatní chyby přenosu (zrušené spojení apod.) bereme jako dočasné.
	return true
}

// UnexpectedResponseError vznikne, když server vrátí odpověď, kterou nelze
// zpracovat (jiný HTTP stav, SOAP Fault, neúplné XML).
type UnexpectedResponseError struct {
	StatusCode          int
	Msg                 string
	GlobalTransactionID string
}

func (e *UnexpectedResponseError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("eet2: neočekávaná odpověď (HTTP %d): %s", e.StatusCode, e.Msg)
	}
	return "eet2: neočekávaná odpověď: " + e.Msg
}

type xmlEnvelope struct {
	Body struct {
		Odpoved *xmlOdpoved `xml:"Odpoved"`
		Fault   *struct {
			Code   string `xml:"faultcode"`
			String string `xml:"faultstring"`
		} `xml:"Fault"`
	} `xml:"Body"`
}

type xmlOdpoved struct {
	Hlavicka struct {
		UuidZpravy string `xml:"uuid_zpravy,attr"`
		DatPrij    string `xml:"dat_prij,attr"`
		DatOdmit   string `xml:"dat_odmit,attr"`
	} `xml:"Hlavicka"`
	Potvrzeni *struct {
		Pok  string `xml:"pok,attr"`
		Test string `xml:"test,attr"`
	} `xml:"Potvrzeni"`
	Chyba *struct {
		Kod  string `xml:"kod,attr"`
		Test string `xml:"test,attr"`
		Text string `xml:",chardata"`
	} `xml:"Chyba"`
	Varovani []struct {
		Kod  string `xml:"kod_varov,attr"`
		Text string `xml:",chardata"`
	} `xml:"Varovani"`
}

func parseBool(s string) bool { return s == "true" || s == "1" }

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// parseResponse zpracuje tělo HTTP odpovědi. sentUUID je UUID odeslané
// zprávy a slouží ke kontrole, že odpověď patří k ní.
func parseResponse(body []byte, status int, sentUUID, gtid string) (*Response, error) {
	var env xmlEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, &UnexpectedResponseError{status, "nelze přečíst XML: " + err.Error(), gtid}
	}
	if f := env.Body.Fault; f != nil {
		return nil, &UnexpectedResponseError{status, "SOAP Fault " + f.Code + ": " + strings.TrimSpace(f.String), gtid}
	}
	o := env.Body.Odpoved
	if o == nil {
		return nil, &UnexpectedResponseError{status, "odpověď neobsahuje element Odpoved", gtid}
	}

	var warnings []Varovani
	for _, v := range o.Varovani {
		k, _ := strconv.Atoi(strings.TrimSpace(v.Kod))
		warnings = append(warnings, Varovani{Kod: k, Text: strings.TrimSpace(v.Text)})
	}

	if c := o.Chyba; c != nil {
		kod, err := strconv.Atoi(strings.TrimSpace(c.Kod))
		if err != nil {
			return nil, &UnexpectedResponseError{status, "neplatný kód chyby " + strconv.Quote(c.Kod), gtid}
		}
		if kod == ErrKodOvereniOK {
			return &Response{
				UuidZpravy:          sentUUID,
				Test:                parseBool(c.Test),
				Overeno:             true,
				Varovani:            warnings,
				GlobalTransactionID: gtid,
			}, nil
		}
		return nil, &Error{
			Kod:                 kod,
			Text:                strings.TrimSpace(c.Text),
			Test:                parseBool(c.Test),
			UuidZpravy:          o.Hlavicka.UuidZpravy,
			DatOdmit:            parseTime(o.Hlavicka.DatOdmit),
			GlobalTransactionID: gtid,
		}
	}

	p := o.Potvrzeni
	if p == nil {
		return nil, &UnexpectedResponseError{status, "odpověď neobsahuje potvrzení ani chybu", gtid}
	}
	if !rePOK.MatchString(p.Pok) {
		return nil, &UnexpectedResponseError{status, "neplatný formát POK " + strconv.Quote(p.Pok), gtid}
	}
	if !strings.EqualFold(o.Hlavicka.UuidZpravy, sentUUID) {
		return nil, &UnexpectedResponseError{status, "UUID v odpovědi " + strconv.Quote(o.Hlavicka.UuidZpravy) +
			" neodpovídá odeslané zprávě " + strconv.Quote(sentUUID), gtid}
	}
	return &Response{
		UuidZpravy:          o.Hlavicka.UuidZpravy,
		DatPrij:             parseTime(o.Hlavicka.DatPrij),
		Pok:                 p.Pok,
		Test:                parseBool(p.Test),
		Varovani:            warnings,
		GlobalTransactionID: gtid,
	}, nil
}
