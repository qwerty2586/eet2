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

// Response is a successful reply from EET.
type Response struct {
	// UuidZpravy is the UUID of the sent message (uuid_zpravy).
	UuidZpravy string

	// DatPrij is when the server received the message (dat_prij).
	DatPrij time.Time

	// Pok is the confirmation code (pok, 39 characters). Empty in
	// verification mode. In the non-production environment it ends with
	// "-ff" and is not valid.
	Pok string

	// Test is true if the reply came from the non-production environment
	// (playground).
	Test bool

	// Verified is true in verification mode when the message passed all
	// checks (the server returned error code 0). The sale is NOT registered.
	Verified bool

	// Warnings are non-critical errors. The POK is valid even with warnings,
	// but the data is worth fixing.
	Warnings []Warning

	// GlobalTransactionID is the X-Global-Transaction-Id header, useful when
	// tracking down problems with the tax administration's support.
	GlobalTransactionID string
}

// Warning is a non-critical error (varovani) from the reply.
type Warning struct {
	Code int
	Text string
}

// Error is an error reply from EET: a critical error in the message or a
// temporary technical failure on the server side.
type Error struct {
	Code                int
	Text                string
	Test                bool
	UuidZpravy          string
	DatOdmit            time.Time
	GlobalTransactionID string
}

func (e *Error) Error() string {
	return fmt.Sprintf("eet2: EET error %d: %s", e.Code, e.Text)
}

// Temporary is true for a temporary technical failure on the server
// (negative code). The message must be sent again later.
func (e *Error) Temporary() bool { return e.Code < 0 }

// Error codes per the specification, ch. 3.5.4.
const (
	ErrCodeTemporary  = -1 // temporary technical failure, send again later
	ErrCodeVerifiedOK = 0  // verification mode: message processed successfully
	ErrCodeEncoding   = 2  // invalid XML encoding
	ErrCodeSchema     = 3  // message failed XML schema validation
	ErrCodeSignature  = 4  // invalid SOAP message signature
	ErrCodeEIC        = 6  // malformed taxpayer EIČ
	ErrCodeTooLarge   = 7  // message too large
	ErrCodeProcessing = 8  // technical or data error
)

// IsRetryable reports whether the same sale should be sent again
// (with Resend: true): a connection failure, timeout or temporary server
// error. It returns false for validation errors and critical message errors.
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
	// Other transport errors (dropped connection etc.) are treated as temporary.
	return true
}

// UnexpectedResponseError means the server returned a reply that cannot be
// processed (unexpected HTTP status, SOAP Fault, incomplete XML).
type UnexpectedResponseError struct {
	StatusCode          int
	Msg                 string
	GlobalTransactionID string
}

func (e *UnexpectedResponseError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("eet2: unexpected response (HTTP %d): %s", e.StatusCode, e.Msg)
	}
	return "eet2: unexpected response: " + e.Msg
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

// parseResponse processes the HTTP response body. sentUUID is the UUID of
// the sent message, used to check that the reply belongs to it.
func parseResponse(body []byte, status int, sentUUID, gtid string) (*Response, error) {
	var env xmlEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, &UnexpectedResponseError{status, "cannot parse XML: " + err.Error(), gtid}
	}
	if f := env.Body.Fault; f != nil {
		return nil, &UnexpectedResponseError{status, "SOAP Fault " + f.Code + ": " + strings.TrimSpace(f.String), gtid}
	}
	o := env.Body.Odpoved
	if o == nil {
		return nil, &UnexpectedResponseError{status, "response has no Odpoved element", gtid}
	}

	var warnings []Warning
	for _, v := range o.Varovani {
		k, _ := strconv.Atoi(strings.TrimSpace(v.Kod))
		warnings = append(warnings, Warning{Code: k, Text: strings.TrimSpace(v.Text)})
	}

	if c := o.Chyba; c != nil {
		code, err := strconv.Atoi(strings.TrimSpace(c.Kod))
		if err != nil {
			return nil, &UnexpectedResponseError{status, "invalid error code " + strconv.Quote(c.Kod), gtid}
		}
		if code == ErrCodeVerifiedOK {
			return &Response{
				UuidZpravy:          sentUUID,
				Test:                parseBool(c.Test),
				Verified:            true,
				Warnings:            warnings,
				GlobalTransactionID: gtid,
			}, nil
		}
		return nil, &Error{
			Code:                code,
			Text:                strings.TrimSpace(c.Text),
			Test:                parseBool(c.Test),
			UuidZpravy:          o.Hlavicka.UuidZpravy,
			DatOdmit:            parseTime(o.Hlavicka.DatOdmit),
			GlobalTransactionID: gtid,
		}
	}

	p := o.Potvrzeni
	if p == nil {
		return nil, &UnexpectedResponseError{status, "response has neither confirmation nor error", gtid}
	}
	if !rePOK.MatchString(p.Pok) {
		return nil, &UnexpectedResponseError{status, "invalid POK format " + strconv.Quote(p.Pok), gtid}
	}
	if !strings.EqualFold(o.Hlavicka.UuidZpravy, sentUUID) {
		return nil, &UnexpectedResponseError{status, "response UUID " + strconv.Quote(o.Hlavicka.UuidZpravy) +
			" does not match the sent message " + strconv.Quote(sentUUID), gtid}
	}
	return &Response{
		UuidZpravy:          o.Hlavicka.UuidZpravy,
		DatPrij:             parseTime(o.Hlavicka.DatPrij),
		Pok:                 p.Pok,
		Test:                parseBool(p.Test),
		Warnings:            warnings,
		GlobalTransactionID: gtid,
	}, nil
}
