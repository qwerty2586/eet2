# Connection failures and resending

Communication with EET is synchronous (request/response). When no reply arrives, there is no way
to tell whether the server accepted the sale. It must then be sent again **with the same data**
and `Resend` set. A new UUID is generated automatically. The server recognises a resend of the
same sale and does not count it twice.

```go
resp, err := d.SendPayment(r)
if eet2.IsRetryable(err) {
	// network failure, timeout or temporary server error (code -1):
	// queue r and send it again without undue delay,
	// within 48 hours at the latest
	r.Resend = true
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
