# issue ENG-101

| Field | Value |
| --- | --- |
| key | ENG-101 |
| summary | Retry logic drops the last error |
| status | In Progress |

## description

## Repro

```go
client.Do(req)  // returns err == nil on 5xx
```

Also: a < b && c > d, and a literal ]]> in the text.

## labels

- retry
- transport

## components

_None._

