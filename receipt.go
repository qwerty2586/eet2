package eet2

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
	_ "time/tzdata" // časová zóna Europe/Prague i na systémech bez tzdata
)

// Receipt jsou údaje o jedné evidované tržbě.
//
// Povinné jsou EicPopl, IdJednotky, IdPokl, PoradCis, DatTrzby a CelkTrzba.
// Ostatní položky vyplňte, jen když jsou pro tržbu relevantní.
type Receipt struct {
	// UuidZpravy je UUID datové zprávy. Nechte prázdné: pro každé odeslání
	// se vygeneruje nové (i opakované odeslání musí mít nové UUID).
	UuidZpravy string

	// Opakovane označuje opakované zaslání téže tržby (prvni_zaslani=false),
	// typicky po výpadku spojení. Data tržby se přitom nesmí měnit.
	Opakovane bool

	// Overeni zapne ověřovací mód: server zprávu jen zkontroluje a neeviduje ji.
	Overeni bool

	// EicPopl je evidenční identifikační číslo poplatníka, např. "CZ00000019".
	EicPopl string

	// EicPoverujiciho je EIČ poplatníka, který evidováním pověřil EicPopl.
	EicPoverujiciho string

	// PovereniVicePopl značí, že tržba plyne více poplatníkům.
	PovereniVicePopl bool

	// IdJednotky je číslo evidenční jednotky přidělené v DIS+ (1–999999999).
	IdJednotky int

	// IdPokl je označení pokladního zařízení (1–20 znaků).
	IdPokl string

	// PoradCis je pořadové číslo tržby (1–25 znaků).
	PoradCis string

	// DatTrzby je okamžik uskutečnění tržby. Odešle se v časové zóně
	// Europe/Prague (+01:00 / +02:00).
	DatTrzby time.Time

	// CelkTrzba je celková částka tržby v Kč včetně daní. Záporná u storna.
	CelkTrzba float64

	// UrcenoCerpZuct je částka určená k následnému čerpání nebo zúčtování
	// (nabití kreditu). Nil = položka se neuvádí. Viz Castka.
	UrcenoCerpZuct *float64

	// CerpZuct je částka, která je následným čerpáním nebo zúčtováním
	// (čerpání kreditu). Nil = položka se neuvádí. Viz Castka.
	CerpZuct *float64
}

// Castka vrátí ukazatel na částku pro nepovinné položky UrcenoCerpZuct a CerpZuct.
func Castka(v float64) *float64 { return &v }

// ValidationError popisuje údaj, který neodpovídá formátu ze specifikace.
// Takovou zprávu nemá smysl odesílat ani opakovat.
type ValidationError struct {
	Field string
	Value string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("eet2: neplatná položka %s=%q: %s", e.Field, e.Value, e.Msg)
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

// formatTime vrátí čas ve tvaru rrrr-mm-ddThh:mm:ss±hh:mm v zóně Europe/Prague.
func formatTime(t time.Time) string {
	return t.In(prague).Format("2006-01-02T15:04:05-07:00")
}

// formatAmount převede částku na tvar ze specifikace: přesně dvě desetinná
// místa, tečka, bez vedoucích nul, bez "-0.00", |v| < 100 000 000.
func formatAmount(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", errors.New("částka není konečné číslo")
	}
	c := math.Round(v * 100)
	if math.Abs(c) >= 1e10 {
		return "", errors.New("částka musí být v absolutní hodnotě menší než 100 000 000 Kč")
	}
	h := int64(c)
	sign := ""
	if h < 0 {
		sign = "-"
		h = -h
	}
	return fmt.Sprintf("%s%d.%02d", sign, h/100, h%100), nil
}

// NewUUID vrátí náhodné UUID verze 4.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
}

// Validate zkontroluje údaje tržby proti formátům ze specifikace.
func (r Receipt) Validate() error {
	_, _, err := r.attrs(time.Now())
	return err
}

// hlavicka a data jsou atributy XML elementů ve formě (jméno, hodnota).
type attr struct{ name, value string }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// attrs převede tržbu na atributy elementů Hlavicka a Data (hodnoty už ve
// formátu ze specifikace) a zároveň je zkontroluje.
func (r Receipt) attrs(now time.Time) (hlavicka, data []attr, err error) {
	uuid := r.UuidZpravy
	if uuid == "" {
		uuid = NewUUID()
	}
	if !reUUID.MatchString(uuid) {
		return nil, nil, &ValidationError{"uuid_zpravy", uuid, "neodpovídá formátu UUID"}
	}
	hlavicka = []attr{
		{"uuid_zpravy", uuid},
		{"dat_odesl", formatTime(now)},
		{"prvni_zaslani", boolStr(!r.Opakovane)},
	}
	if r.Overeni {
		hlavicka = append(hlavicka, attr{"overeni", "true"})
	}

	if !reEIC.MatchString(r.EicPopl) {
		return nil, nil, &ValidationError{"eic_popl", r.EicPopl, "očekáván tvar CZ + 8 až 10 číslic"}
	}
	data = append(data, attr{"eic_popl", r.EicPopl})
	if r.EicPoverujiciho != "" {
		if !reEIC.MatchString(r.EicPoverujiciho) {
			return nil, nil, &ValidationError{"eic_poverujiciho", r.EicPoverujiciho, "očekáván tvar CZ + 8 až 10 číslic"}
		}
		data = append(data, attr{"eic_poverujiciho", r.EicPoverujiciho})
	}
	if r.PovereniVicePopl {
		data = append(data, attr{"povereni_vice_popl", "true"})
	}
	if r.IdJednotky < 1 || r.IdJednotky > 999999999 {
		return nil, nil, &ValidationError{"id_jednotky", strconv.Itoa(r.IdJednotky), "musí být 1 až 999999999"}
	}
	data = append(data, attr{"id_jednotky", strconv.Itoa(r.IdJednotky)})
	if !reIdPokl.MatchString(r.IdPokl) {
		return nil, nil, &ValidationError{"id_pokl", r.IdPokl, "1–20 znaků z 0-9 a-z A-Z . , : ; / # - _ a mezery"}
	}
	data = append(data, attr{"id_pokl", r.IdPokl})
	if !rePoradCis.MatchString(r.PoradCis) {
		return nil, nil, &ValidationError{"porad_cis", r.PoradCis, "1–25 znaků z 0-9 a-z A-Z . , : ; / # - _ a mezery"}
	}
	data = append(data, attr{"porad_cis", r.PoradCis})
	if r.DatTrzby.IsZero() {
		return nil, nil, &ValidationError{"dat_trzby", "", "chybí datum a čas tržby"}
	}
	data = append(data, attr{"dat_trzby", formatTime(r.DatTrzby)})

	amounts := []struct {
		name string
		v    *float64
	}{
		{"celk_trzba", &r.CelkTrzba},
		{"urceno_cerp_zuct", r.UrcenoCerpZuct},
		{"cerp_zuct", r.CerpZuct},
	}
	for _, a := range amounts {
		if a.v == nil {
			continue
		}
		s, err := formatAmount(*a.v)
		if err != nil {
			return nil, nil, &ValidationError{a.name, fmt.Sprint(*a.v), err.Error()}
		}
		data = append(data, attr{a.name, s})
	}
	return hlavicka, data, nil
}

// UniqueKey vrátí řetězec, podle kterého systém EET pozná téže tržbu
// (eic|jednotka|pokladna|pořadové číslo|datum a čas|částka). Hodí se jako
// klíč fronty pro opakované odesílání.
func (r Receipt) UniqueKey() (string, error) {
	amount, err := formatAmount(r.CelkTrzba)
	if err != nil {
		return "", err
	}
	return r.EicPopl + "|" + strconv.Itoa(r.IdJednotky) + "|" + r.IdPokl + "|" +
		r.PoradCis + "|" + formatTime(r.DatTrzby) + "|" + amount, nil
}
