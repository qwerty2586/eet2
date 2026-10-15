package eet2

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strings"
)

// Jmenné prostory a algoritmy podle technické specifikace EET 2.0, kap. 5.
const (
	nsSoap = "http://schemas.xmlsoap.org/soap/envelope/"
	nsEET  = "http://fs.gov.cz/eet/schema/v4"
	nsWsse = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	nsWsu  = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"
	nsDs   = "http://www.w3.org/2000/09/xmldsig#"
	nsEc   = "http://www.w3.org/2001/10/xml-exc-c14n#"

	algC14N      = "http://www.w3.org/2001/10/xml-exc-c14n#"
	algSHA256    = "http://www.w3.org/2001/04/xmlenc#sha256"
	algRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"

	encBase64 = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary"
	valX509v3 = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-x509-token-profile-1.0#X509v3"

	// SOAPAction podle EETServiceSOAP.wsdl.
	soapAction = "http://fs.gov.cz/eet/OdeslaniTrzby"

	// Limit velikosti celé zprávy včetně SOAP obálky (kritická kontrola č. 5).
	maxMessageSize = 12 * 1024
)

// escapeAttr escapuje hodnotu atributu podle pravidel kanonizace XML.
// Validace sice tyto znaky nepouští, ale jistota je jistota.
func escapeAttr(s string) string {
	if !strings.ContainsAny(s, "&<\"\t\n\r") {
		return s
	}
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", `"`, "&quot;",
		"\t", "&#x9;", "\n", "&#xA;", "\r", "&#xD;")
	return r.Replace(s)
}

// element vrátí prázdný element s atributy seřazenými podle jména,
// v kanonickém tvaru (explicitní koncová značka).
func element(name string, attrs []attr) string {
	sorted := append([]attr(nil), attrs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	var b strings.Builder
	b.WriteString("<" + name)
	for _, a := range sorted {
		b.WriteString(" " + a.name + `="` + escapeAttr(a.value) + `"`)
	}
	b.WriteString("></" + name + ">")
	return b.String()
}

// trzbaXML vrátí element v4:Trzba (shodný v dokumentu i v kanonickém tvaru).
func trzbaXML(hlavicka, data []attr) string {
	return "<v4:Trzba>" + element("v4:Hlavicka", hlavicka) + element("v4:Data", data) + "</v4:Trzba>"
}

// canonicalBody vrátí soapenv:Body po kanonizaci Exclusive C14N
// s InclusiveNamespaces PrefixList="v4". Z tohoto tvaru se počítá DigestValue.
func canonicalBody(bodyID, trzba string) string {
	return `<soapenv:Body xmlns:soapenv="` + nsSoap + `" xmlns:v4="` + nsEET + `" xmlns:wsu="` + nsWsu +
		`" wsu:Id="` + bodyID + `">` + trzba + `</soapenv:Body>`
}

// documentBody vrátí soapenv:Body tak, jak se vloží do obálky
// (soapenv a v4 jsou deklarovány na Envelope).
func documentBody(bodyID, trzba string) string {
	return `<soapenv:Body xmlns:wsu="` + nsWsu + `" wsu:Id="` + bodyID + `">` + trzba + `</soapenv:Body>`
}

// signedInfoInner vrátí obsah ds:SignedInfo (bez obalového elementu).
func signedInfoInner(bodyID, digest string) string {
	return `<ds:CanonicalizationMethod Algorithm="` + algC14N + `">` +
		`<ec:InclusiveNamespaces xmlns:ec="` + nsEc + `" PrefixList="soapenv v4"></ec:InclusiveNamespaces>` +
		`</ds:CanonicalizationMethod>` +
		`<ds:SignatureMethod Algorithm="` + algRSASHA256 + `"></ds:SignatureMethod>` +
		`<ds:Reference URI="#` + bodyID + `"><ds:Transforms><ds:Transform Algorithm="` + algC14N + `">` +
		`<ec:InclusiveNamespaces xmlns:ec="` + nsEc + `" PrefixList="v4"></ec:InclusiveNamespaces>` +
		`</ds:Transform></ds:Transforms>` +
		`<ds:DigestMethod Algorithm="` + algSHA256 + `"></ds:DigestMethod>` +
		`<ds:DigestValue>` + digest + `</ds:DigestValue></ds:Reference>`
}

// canonicalSignedInfo vrátí ds:SignedInfo po kanonizaci Exclusive C14N
// s PrefixList="soapenv v4". Tento tvar se podepisuje.
func canonicalSignedInfo(bodyID, digest string) string {
	return `<ds:SignedInfo xmlns:ds="` + nsDs + `" xmlns:soapenv="` + nsSoap + `" xmlns:v4="` + nsEET + `">` +
		signedInfoInner(bodyID, digest) + `</ds:SignedInfo>`
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

func b64sha256(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(h[:])
}

// buildEnvelope sestaví a podepíše kompletní SOAP zprávu.
func buildEnvelope(hlavicka, data []attr, cert *x509.Certificate, key crypto.Signer) ([]byte, error) {
	id := randomID()
	bodyID := "id-" + id
	tokenID := "X509-" + id
	trzba := trzbaXML(hlavicka, data)

	digest := b64sha256(canonicalBody(bodyID, trzba))
	si := sha256.Sum256([]byte(canonicalSignedInfo(bodyID, digest)))
	sig, err := key.Sign(rand.Reader, si[:], crypto.SHA256)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	b.Grow(6 * 1024)
	b.WriteString(`<soapenv:Envelope xmlns:soapenv="` + nsSoap + `" xmlns:v4="` + nsEET + `">`)
	b.WriteString(`<soapenv:Header><wsse:Security xmlns:wsse="` + nsWsse + `" xmlns:wsu="` + nsWsu + `">`)
	b.WriteString(`<wsse:BinarySecurityToken EncodingType="` + encBase64 + `" ValueType="` + valX509v3 +
		`" wsu:Id="` + tokenID + `">` + base64.StdEncoding.EncodeToString(cert.Raw) + `</wsse:BinarySecurityToken>`)
	b.WriteString(`<ds:Signature Id="SIG-` + id + `" xmlns:ds="` + nsDs + `">`)
	b.WriteString(`<ds:SignedInfo>` + signedInfoInner(bodyID, digest) + `</ds:SignedInfo>`)
	b.WriteString(`<ds:SignatureValue>` + base64.StdEncoding.EncodeToString(sig) + `</ds:SignatureValue>`)
	b.WriteString(`<ds:KeyInfo Id="KI-` + id + `"><wsse:SecurityTokenReference wsu:Id="STR-` + id + `">` +
		`<wsse:Reference URI="#` + tokenID + `" ValueType="` + valX509v3 + `"></wsse:Reference>` +
		`</wsse:SecurityTokenReference></ds:KeyInfo>`)
	b.WriteString(`</ds:Signature></wsse:Security></soapenv:Header>`)
	b.WriteString(documentBody(bodyID, trzba))
	b.WriteString(`</soapenv:Envelope>`)

	out := []byte(`<?xml version="1.0" encoding="UTF-8"?>` + b.String())
	if len(out) > maxMessageSize {
		return nil, &ValidationError{"zprava", "", "datová zpráva přesahuje 12 kB"}
	}
	return out, nil
}
