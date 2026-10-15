# Preventing collisions

The specification requires the combination `eic_popl`, `id_jednotky`, `id_pokl`, `dat_trzby` to be
unique: one cash register (`id_pokl`) may not register two sales in the same second within a
registration unit. With a single physical register this never happens, but it can when several
users share one `id_pokl`, e.g. in a web application.

`Receipt.RegisterKey()` is a helper for preventing such collisions. It returns the combination as
one string, with the values normalised exactly as they are sent:

```
CZ00000019*181*RECEPCE*2027-01-08T21:19:38+01:00
```

`dat_trzby` is in Europe/Prague with whole seconds, so `time.Now()` with milliseconds or in UTC
gives the same key as the server sees. The separator `*` is not allowed in any of the fields, so
different combinations never produce the same key.

## Usage with a database

Store the key in a column with a unique index and let the database detect collisions, which also
works with concurrent writes:

```sql
CREATE UNIQUE INDEX sales_register_key ON sales (register_key);
```

```go
for _, pokl := range []string{"WEB1", "WEB2", "WEB3"} {
	r.IdPokl = pokl
	key, err := r.RegisterKey()
	if err != nil {
		return err
	}
	err = db.InsertSale(ctx, r, key) // INSERT … register_key = key
	if isUniqueViolation(err) {
		continue // this id_pokl already has a sale in this second, try the next one
	}
	if err != nil {
		return err
	}
	return send(r)
}
return errors.New("all registers busy in this second")
```

Uniqueness of `RegisterKey` also guarantees the second rule of the specification, uniqueness of
(`eic_popl`, `id_jednotky`, `id_pokl`, `porad_cis`, `dat_trzby`), because that one only adds
`porad_cis`.

Do not confuse it with `UniqueKey()`, which also includes `porad_cis` and the amount: that one
identifies the same sale when it is resent (see [retries.md](retries.md)).
