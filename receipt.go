package eet2

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
	_ "time/tzdata" // Europe/Prague even on systems without tzdata

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Receipt is one registered sale (evidovaná tržba).
//
// Field names follow the EET data message attributes. Required fields are
// EicPopl, IdJednotky, IdPokl, PoradCis, DatTrzby and CelkTrzba; set the
// others only when they apply to the sale.
type Receipt struct {
	// UuidZpravy is the message UUID (uuid_zpravy). Leave it empty: a fresh
	// UUID is generated for every send, which the spec requires even for
	// resends.
	UuidZpravy string

	// Opakovane marks a repeated send of the same sale (prvni_zaslani=false),
	// typically after a connection failure. The sale data must not change.
	Opakovane bool

	// Overeni enables verification mode (overeni): the server only checks
	// the message and does not register the sale.
	Overeni bool

	// EicPopl is the taxpayer's registration number (eic_popl), e.g. "CZ00000019".
	EicPopl string

	// EicPoverujiciho is the EIČ of the taxpayer who authorised EicPopl to
	// register sales on their behalf (eic_poverujiciho).
	EicPoverujiciho string

	// PovereniVicePopl marks a sale that belongs to several taxpayers
	// (povereni_vice_popl).
	PovereniVicePopl bool

	// IdJednotky is the registration unit number assigned in DIS+
	// (id_jednotky, 1–999999999).
	IdJednotky int

	// IdPokl identifies the cash register (id_pokl, 1–20 characters).
	IdPokl string

	// PoradCis is the sale's sequence number (porad_cis, 1–25 characters).
	PoradCis string

	// DatTrzby is when the sale took place (dat_trzby). It is sent in the
	// Europe/Prague time zone (+01:00 / +02:00).
	DatTrzby time.Time

	// CelkTrzba is the total amount in CZK including taxes (celk_trzba),
	// negative for refunds. At most two decimal places: rounding is up to
	// the application so the registered amount matches the receipt
	// (see docs/amounts.md).
	CelkTrzba decimal.Decimal

	// UrcenoCerpZuct is the amount intended for later drawing or settlement,
	// e.g. topping up credit (urceno_cerp_zuct). Valid=false omits it.
	UrcenoCerpZuct decimal.NullDecimal

	// CerpZuct is the amount drawn from earlier credit (cerp_zuct).
	// Valid=false omits it.
	CerpZuct decimal.NullDecimal
}

// ValidationError reports a field that does not match the format required
// by the specification. Such a message is never sent and must not be retried.
type ValidationError struct {
	Field string
	Value string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("eet2: invalid %s=%q: %s", e.Field, e.Value, e.Msg)
}

var (
	reEIC      = regexp.MustCompile(`^CZ[0-9]{8,10}$`)
	reIdPokl   = regexp.MustCompile(`^[0-9a-zA-Z\.,:;/#\-_ ]{1,20}$`)
	rePoradCis = regexp.MustCompile(`^[0-9a-zA-Z\.,:;/#\-_ ]{1,25}$`)
	reUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	rePOK      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}-[0-9a-fA-F]{2}$`)
)

var prague = mustLoadPrague()

func mustLoadPrague() *time.Location {
	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		panic(err)
	}
	return loc
}

// formatTime returns yyyy-mm-ddThh:mm:ss±hh:mm in Europe/Prague.
func formatTime(t time.Time) string {
	return t.In(prague).Format("2006-01-02T15:04:05-07:00")
}

var maxAmount = decimal.NewFromInt(100_000_000)

// formatAmount renders an amount as the spec requires: exactly two decimal
// places, a dot, no leading zeros, no "-0.00", |v| < 100 000 000.
// Amounts with more than two decimal places are rejected, never rounded.
func formatAmount(v decimal.Decimal) (string, error) {
	if !v.Equal(v.Truncate(2)) {
		return "", errors.New("amount has more than two decimal places, round it before sending (see docs/amounts.md)")
	}
	if v.Abs().Cmp(maxAmount) >= 0 {
		return "", errors.New("amount must be less than 100 000 000 CZK in absolute value")
	}
	return v.StringFixed(2), nil
}

// NewUUID returns a random version 4 UUID (github.com/google/uuid).
//
// EET accepts only UUID versions 1 to 5, so do not use version 7 for
// UuidZpravy.
func NewUUID() string {
	return uuid.NewString()
}

// Validate checks the sale against the formats from the specification.
func (r Receipt) Validate() error {
	_, _, err := r.attrs(time.Now())
	return err
}

// attr is one XML attribute (name, value).
type attr struct{ name, value string }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// attrs converts the sale into attributes of the Hlavicka (header) and Data
// elements, already formatted per the spec, and validates them.
func (r Receipt) attrs(now time.Time) (header, data []attr, err error) {
	uuid := r.UuidZpravy
	if uuid == "" {
		uuid = NewUUID()
	}
	if !reUUID.MatchString(uuid) {
		return nil, nil, &ValidationError{"uuid_zpravy", uuid, "not a valid UUID"}
	}
	header = []attr{
		{"uuid_zpravy", uuid},
		{"dat_odesl", formatTime(now)},
		{"prvni_zaslani", boolStr(!r.Opakovane)},
	}
	if r.Overeni {
		header = append(header, attr{"overeni", "true"})
	}

	if !reEIC.MatchString(r.EicPopl) {
		return nil, nil, &ValidationError{"eic_popl", r.EicPopl, "expected CZ followed by 8 to 10 digits"}
	}
	data = append(data, attr{"eic_popl", r.EicPopl})
	if r.EicPoverujiciho != "" {
		if !reEIC.MatchString(r.EicPoverujiciho) {
			return nil, nil, &ValidationError{"eic_poverujiciho", r.EicPoverujiciho, "expected CZ followed by 8 to 10 digits"}
		}
		data = append(data, attr{"eic_poverujiciho", r.EicPoverujiciho})
	}
	if r.PovereniVicePopl {
		data = append(data, attr{"povereni_vice_popl", "true"})
	}
	if r.IdJednotky < 1 || r.IdJednotky > 999999999 {
		return nil, nil, &ValidationError{"id_jednotky", strconv.Itoa(r.IdJednotky), "must be 1 to 999999999"}
	}
	data = append(data, attr{"id_jednotky", strconv.Itoa(r.IdJednotky)})
	if !reIdPokl.MatchString(r.IdPokl) {
		return nil, nil, &ValidationError{"id_pokl", r.IdPokl, "1–20 characters from 0-9 a-z A-Z . , : ; / # - _ and space"}
	}
	data = append(data, attr{"id_pokl", r.IdPokl})
	if !rePoradCis.MatchString(r.PoradCis) {
		return nil, nil, &ValidationError{"porad_cis", r.PoradCis, "1–25 characters from 0-9 a-z A-Z . , : ; / # - _ and space"}
	}
	data = append(data, attr{"porad_cis", r.PoradCis})
	if r.DatTrzby.IsZero() {
		return nil, nil, &ValidationError{"dat_trzby", "", "sale date and time is missing"}
	}
	data = append(data, attr{"dat_trzby", formatTime(r.DatTrzby)})

	amounts := []struct {
		name string
		v    decimal.NullDecimal
	}{
		{"celk_trzba", decimal.NewNullDecimal(r.CelkTrzba)},
		{"urceno_cerp_zuct", r.UrcenoCerpZuct},
		{"cerp_zuct", r.CerpZuct},
	}
	for _, a := range amounts {
		if !a.v.Valid {
			continue
		}
		s, err := formatAmount(a.v.Decimal)
		if err != nil {
			return nil, nil, &ValidationError{a.name, a.v.Decimal.String(), err.Error()}
		}
		data = append(data, attr{a.name, s})
	}
	return header, data, nil
}

// UniqueKey returns the string by which EET recognises the same sale
// (eic|unit|register|sequence number|date and time|amount). Useful as
// a key for a resend queue.
func (r Receipt) UniqueKey() (string, error) {
	amount, err := formatAmount(r.CelkTrzba)
	if err != nil {
		return "", err
	}
	return r.EicPopl + "|" + strconv.Itoa(r.IdJednotky) + "|" + r.IdPokl + "|" +
		r.PoradCis + "|" + formatTime(r.DatTrzby) + "|" + amount, nil
}

// RegisterKey is a helper for preventing collisions. It returns
// "eic_popl*id_jednotky*id_pokl*dat_trzby" with the values normalised exactly
// as they are sent (dat_trzby in Europe/Prague, whole seconds).
//
// The specification requires this combination to be unique: one id_pokl
// may not register two sales in the same second within a registration unit.
// Store it in a column with a unique index, and when an insert fails on it,
// assign another id_pokl (or wait a second) before sending. Uniqueness of
// this key also guarantees uniqueness of the porad_cis combination, which
// only adds porad_cis to it.
//
// The separator "*" cannot appear in any of the fields, so two different
// combinations never produce the same key. Unlike UniqueKey, it does not
// include porad_cis or the amount.
func (r Receipt) RegisterKey() (string, error) {
	if !reEIC.MatchString(r.EicPopl) {
		return "", &ValidationError{"eic_popl", r.EicPopl, "expected CZ followed by 8 to 10 digits"}
	}
	if r.IdJednotky < 1 || r.IdJednotky > 999999999 {
		return "", &ValidationError{"id_jednotky", strconv.Itoa(r.IdJednotky), "must be 1 to 999999999"}
	}
	if !reIdPokl.MatchString(r.IdPokl) {
		return "", &ValidationError{"id_pokl", r.IdPokl, "1–20 characters from 0-9 a-z A-Z . , : ; / # - _ and space"}
	}
	if r.DatTrzby.IsZero() {
		return "", &ValidationError{"dat_trzby", "", "sale date and time is missing"}
	}
	return r.EicPopl + "*" + strconv.Itoa(r.IdJednotky) + "*" + r.IdPokl + "*" + formatTime(r.DatTrzby), nil
}
