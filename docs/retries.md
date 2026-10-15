# Connection failures and resending

Communication with EET is synchronous (request/response). When no reply arrives, there is no way
to tell whether the server accepted the sale. It must then be sent again **with the same data**
and `Opakovane` set. A new UUID is generated automatically. The server recognises a resend of the
same sale and does not count it twice.

```go
resp, err := d.SendPayment(r)
if eet2.IsRetryable(err) {
	// network failure, timeout or temporary server error (code -1):
	// queue r and send it again without undue delay,
	// within 48 hours at the latest
	r.Opakovane = true
	queue.Push(r)
}
```

`IsRetryable` returns `false` for validation errors (`*eet2.ValidationError`) and critical message
errors (`*eet2.Error` with a positive code). Retrying will not fix those; the data or configuration
needs to change.

`r.UniqueKey()` makes a good queue key. It is the same combination of fields the server uses to
recognise a sale.

An invalid sale is never sent: the fields are checked against the formats from the specification
first. The server does not reply to some malformed messages at all, which would look like an outage.

## Storing the sale time

`DatTrzby` is a `time.Time`, an absolute instant. The library always converts it to
Europe/Prague using the embedded time zone data, so the system time zone, `TZ` or `time.Local`
do not affect what is sent. It also tells apart the repeated hour when clocks go back
(`02:30+02:00` and `02:30+01:00`).

The risks are at the boundary between the application and its database, and both can make
a resend look like a different sale, so the server registers it twice:

1. **Fractional seconds.** The library drops them (truncates). Some databases round instead:
   MySQL `DATETIME` without fractional seconds stores `21:19:38.750` as `21:19:39`. A resend
   loaded from the database is then one second off from the original. Truncate the time when
   the sale is created, before storing it:

   ```go
   DatTrzby: time.Now().Truncate(time.Second),
   ```

2. **Times without a time zone.** If the database holds Prague wall-clock time without a zone
   (`DATETIME`, `timestamp without time zone`) and the driver reads it as UTC (the MySQL
   driver's default `loc=UTC`), the instant shifts by one or two hours. Store times with a zone
   (`timestamptz`) or in UTC, and configure the driver to read them in the zone they were
   written in.

A simple check: compute `r.UniqueKey()` at the first send, store it, and compare it on resend.

## Response time limit

The default is 3 s; the law requires at least 2 s. Set it with `WithTimeout`:

```go
d, err := eet2.NewDispatcherFromP12(eet2.ProductionService, p12, password,
	eet2.WithTimeout(5*time.Second),
)
```

## Verification mode

Checks the connection and the message format without registering the sale:

```go
r.Overeni = true
resp, err := d.SendPayment(r) // resp.Verified == true, resp.Pok is empty
```
