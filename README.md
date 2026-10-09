# URL shortener

Parts 1–4 are implemented, plus the Part 5 load-balancer/shared-store architecture
proposal and Part 6 graceful shutdown and create rate limiting. The two runtime
backends are memory and file-backed JSON; the distributed architecture is a design,
not a running cluster. Only Go stdlib is used.

## Run

The module currently targets Go 1.27. From the repository root:

```bash
go run -buildvcs=false ./cmd/server -addr :8080 -base http://localhost:8080
```

For links that survive restart:

```bash
go run ./cmd/server -addr :8080 -base http://localhost:8080 \
  -store file -data ./data/links.json
```

| Flag              | Default               | Meaning                                                 |
| ----------------- | --------------------- | ------------------------------------------------------- |
| -addr             | :8080                 | HTTP listen address                                     |
| -base             | http://localhost:8080 | Public URL prefix; trailing slash accepted              |
| -store            | memory                | memory or file                                          |
| -data             | data/links.json       | Snapshot path for the file backend                      |
| -create-rate      | 5                     | POST requests/second per process; 0 disables the limit  |
| -create-burst     | 20                    | Initial and maximum token capacity                      |
| -shutdown-timeout | 10s                   | Maximum drain time before connections are forced closed |

## HTTP API

```bash
curl -i -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{"url":"https://go.dev/doc/"}'
```

Success returns 201 with code and short_url. Repeat the same URL to reuse its code
and original created_at, including after restart with file storage. URL equality
uses exact input strings; URLs with and without a trailing slash are distinct.
Codes currently have six random alphanumeric characters plus a checksum suffix.

Replace CODE with the returned code:

```bash
curl -i http://localhost:8080/CODE
curl -I http://localhost:8080/CODE
curl -L http://localhost:8080/CODE
curl -i http://localhost:8080/api/v1/links/CODE
```

GET/HEAD /CODE returns 302 with Location. Metadata returns 200 with url and
created_at (UTC RFC3339). Unknown codes return 404. Invalid/missing URLs and
malformed JSON return 400; registered endpoints return JSON errors shaped as
{"error":"message"}. Unsupported methods return 405, unexpected storage failures
return 500, and rate-limited creates return 429 plus Retry-After in seconds.
The service validates http/https and an absolute host and never fetches submitted URLs.

## Persistence and restart

The file backend creates parent directories and loads a version-1 JSON snapshot
on startup. Each new URL writes a full candidate snapshot to a temporary file
in the same directory, syncs it, closes it, atomically renames it, and syncs the
parent directory before reporting success. Newly created directories are synced
as well. Snapshot files use mode 0600; new directories use 0700.

Malformed snapshots, unsupported versions, invalid records, or duplicate URLs
cause startup failure. Failed commits before rename preserve the previous file
and in-memory indexes. If rename succeeds but directory sync fails, durability
is uncertain: the store fails closed with 500 responses until restart. Reopen
loads whichever complete snapshot survived; a failed request may have committed,
so clients can safely retry using same-URL idempotency.

Use **one writer process per file path** on a filesystem supporting atomic rename
and file/directory fsync. The backend does not coordinate multiple processes.
Full snapshots take O(number of links) work per new URL and all records are
loaded into memory. This is the assignment backend, not the shared store proposed
for multiple replicas in DECISIONS.md.

To verify restart, create a link, save its code and metadata, stop the server,
restart with the same -data path, then fetch metadata/redirect and shorten the
same URL again. The code and created_at must be unchanged. TODO.md contains
copyable commands for this sequence.

## Graceful shutdown and rate limiting

For direct process control, build and run the binary:

```bash
go build -buildvcs=false -o /tmp/url-shortener ./cmd/server
/tmp/url-shortener -store file -data ./data/links.json
```

Ctrl-C (SIGINT) or SIGTERM stops new connections and lets in-flight handlers
finish for up to -shutdown-timeout. A successful drain exits normally; a deadline
forces connection closure and returns a nonzero exit status. Stop the actual
binary rather than sending a signal only to a go run wrapper. A hard kill does
not drain requests; the file commit protocol still governs recovery.

One mutex-protected token bucket applies to POST /api/shorten across the whole
process. It starts with -create-burst tokens and refills at -create-rate. Invalid
and duplicate POST requests also consume tokens; redirects and metadata do not.
The limiter uses no client-IP map or untrusted forwarded headers. It is global
within a process, so clients share capacity and separate replicas have separate
budgets. Shared/gateway enforcement is part of the future architecture.

## Verification and measurements

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -n1
```

Verified on 2026-10-09: vet, normal tests, and race tests passed. Total statement
coverage is **93.9%** (required minimum 70%). Tests include startup/restart,
failed commits and recovery, HTTP 500 on persistence failure, concurrent durable
writes, bucket refill/concurrency, real-listener request draining and deadlines.
A built-process smoke check also verified SIGTERM/SIGINT, durable restart,
unchanged code/timestamp, redirects, and 429/Retry-After with unaffected reads.

Benchmarks use the memory backend and raw handler/mux paths, excluding the
server's rate-limit middleware, network transport, and file persistence:

```bash
go test -run '^$' -bench=. -benchmem ./...
```

Linux amd64, Go go1.27.0-X:nodwarf5, Intel Core i5-1235U, measured 2026-10-09:

```text
BenchmarkShorten/Duplicate-12   233686   5188 ns/op   6973 B/op   31 allocs/op
BenchmarkShorten/NewURL-12      202114   6406 ns/op   7634 B/op   51 allocs/op
BenchmarkRedirect-12           382574   3146 ns/op   6399 B/op   23 allocs/op
```

Request/recorder construction is included. Setup/seeding is excluded; NewURL
resets its store outside timing every 1024 creates to bound memory. These figures
are not live-network latency or file-backend throughput measurements.

CPU profiling was killed in earlier attempts on this host; allocation profiling
succeeded and supplies the project's allowed similar profiling insight:

```bash
go test -run '^$' -bench=BenchmarkRedirect -benchtime=1s \
  -memprofile=/tmp/url-shortener-memory.out \
  -o /tmp/url-shortener-handler.test ./internal/handler
go tool pprof -top -alloc_space -nodecount=8 \
  /tmp/url-shortener-handler.test /tmp/url-shortener-memory.out
```

Measured insight: bufio.NewReaderSize accounted for **65.69%** of sampled allocated
bytes in the redirect benchmark. The request-construction harness dominates
allocation cost, so these numbers do not establish live-server allocation costs
or CPU bottlenecks. alloc_space measures cumulative allocation, not retained memory.

See TODO.md for grading commands, CHECKLIST.md for verified requirements, and
DECISIONS.md for implementation choices, architecture, limitations, and AI use.
