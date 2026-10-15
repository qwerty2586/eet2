# Výpadek spojení a opakované odeslání

Komunikace s EET je synchronní (request/response). Když odpověď nepřijde, nelze zjistit, jestli
server tržbu přijal. Tržbu je pak potřeba odeslat znovu, **se stejnými daty** a příznakem
`Opakovane`. Nové UUID se vygeneruje samo. Server opakované odeslání téže tržby pozná
a nezapočítá ji dvakrát.

```go
resp, err := d.SendPayment(r)
if eet2.IsRetryable(err) {
	// výpadek sítě, timeout nebo dočasná chyba serveru (kód -1):
	// uložte r do fronty a odešlete znovu bez zbytečného odkladu,
	// nejpozději do 48 hodin
	r.Opakovane = true
	queue.Push(r)
}
```

`IsRetryable` vrací `false` pro chyby validace (`*eet2.ValidationError`) a kritické chyby zprávy
(`*eet2.Error` s kladným kódem). Ty opakováním nezmizí, je potřeba opravit data nebo nastavení.

Jako klíč fronty můžete použít `r.UniqueKey()`. Je to stejná kombinace údajů, podle které
tržbu rozpoznává server.

Neplatná tržba se vůbec neodešle: údaje se před odesláním zkontrolují proti formátům ze
specifikace. Server totiž na některé vadné zprávy neodpovídá vůbec a vypadalo by to jako výpadek.

## Mezní doba odezvy

Výchozí je 3 s, zákon vyžaduje aspoň 2 s. Nastavíte ji přes `WithTimeout`:

```go
d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, heslo,
	eet2.WithTimeout(5*time.Second),
)
```

## Ověřovací mód

Ověří spojení a formát zprávy, tržbu ale neeviduje:

```go
r.Overeni = true
resp, err := d.SendPayment(r) // resp.Overeno == true, resp.Pok je prázdný
```
