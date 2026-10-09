# Grader verification commands and explanations

The mandatory Parts 1–4 and the requested bonuses are implemented. This file is
now a command guide inspired by FINAL-PROJECT.md's grader checks. Run commands
from the repository root in Bash; keep background-process commands in the same
shell so their PID variables remain available. No extra Go dependencies or DB
service are required.

## 1. Format, inspect, test, and measure coverage

```bash
cd /home/sib/Documents/code/go/url-shortener
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -n1
```

- gofmt formats the Go source.
- go vet checks for common mistakes that compile successfully.
- go test runs all unit/integration tests, including temporary-file restart tests.
- -race checks concurrent creation, lookup, durable writes, and rate limiting.
- -coverprofile records statement coverage; the last command prints the total.
  The grader requires at least 70%; the verified total is 93.9%.

Shutdown tests open localhost sockets. If Codex's sandbox blocks them with
"socket: operation not permitted", run the suite in your normal terminal or
approve the requested execution outside the sandbox. They are not skipped.

## 2. Benchmark and inspect a profile

```bash
go test -run '^$' -bench=. -benchmem ./...
```

-run '^$' excludes ordinary tests; -bench=. selects all benchmarks; -benchmem
prints allocated bytes and allocation counts. Duplicate/new URL shortening and
redirect results should appear. Timings vary with load/hardware; there are no
pass/fail thresholds for performance. These benchmarks use memory storage,
handler/mux dispatch, and httptest construction, excluding the rate limiter.

```bash
go test -run '^$' -bench=BenchmarkRedirect -benchtime=1s \
  -memprofile=/tmp/url-shortener-memory.out \
  -o /tmp/url-shortener-handler.test ./internal/handler
go tool pprof -top -alloc_space -nodecount=8 \
  /tmp/url-shortener-handler.test /tmp/url-shortener-memory.out
```

The first command collects sampled allocations; pprof prints the largest
contributors. README.md records the measured request-construction insight.
alloc_space is cumulative allocated space, not retained/peak memory.

Optional CPU-profile equivalent on a host where it works:

```bash
go test -run '^$' -bench=BenchmarkRedirect -benchtime=2s \
  -cpuprofile=/tmp/url-shortener-cpu.out \
  -o /tmp/url-shortener-handler-cpu.test ./internal/handler
go tool pprof -top /tmp/url-shortener-handler-cpu.test /tmp/url-shortener-cpu.out
```

Earlier CPU-profile attempts were killed on this host. The verified allocation
profile satisfies the project's allowance for a similar profiling insight.

## 3. Build and start the durable server

```bash
go build -buildvcs=false -o /tmp/url-shortener ./cmd/server
/tmp/url-shortener -addr :8080 -base http://localhost:8080 \
  -store file -data ./data/grader-links.json -create-rate 0 \
  > /tmp/url-shortener-server.log 2>&1 &
SERVER_PID=$!
cat /tmp/url-shortener-server.log
```

-buildvcs=false avoids metadata stamping when checkout Git metadata is unavailable;
it can be omitted in a normal Git checkout. The binary makes signal delivery
unambiguous. The file path is reused for restart verification; parent directories
are created automatically. Rate limiting is disabled here so API checks cannot
accidentally exhaust the budget. Check for the startup banner before sending
requests. If startup fails, inspect the log (for example, port 8080 may be busy).

Use one writer per file path. Do not start a second file-backed server against
this path while the first one is still running.

For a foreground memory-only run instead:

```bash
go run -buildvcs=false ./cmd/server -addr :8080 -base http://localhost:8080 \
  -store memory
```

Choose the foreground alternative instead of the background server on that port.
It demonstrates the default backend, but memory links do not survive restart.

## 4. Shorten, repeat, redirect, and fetch metadata

With the background durable server from step 3 running:

```bash
curl -sS -D /tmp/shorten.headers -o /tmp/shorten.json \
  -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{"url":"https://go.dev/doc/"}'
cat /tmp/shorten.headers /tmp/shorten.json
CODE=$(python3 -c 'import json; print(json.load(open("/tmp/shorten.json"))["code"])')
```

Expect 201 and JSON containing code and short_url. Python's stdlib helper extracts
the code for later commands; alternatively set CODE manually from the JSON.

```bash
curl -sS -o /tmp/shorten-repeat.json -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{"url":"https://go.dev/doc/"}'
cmp /tmp/shorten.json /tmp/shorten-repeat.json
curl -sS -o /dev/null -D - "http://localhost:8080/$CODE"
curl -I "http://localhost:8080/$CODE"
curl -sS -D /tmp/metadata.headers -o /tmp/metadata-before.json \
  "http://localhost:8080/api/v1/links/$CODE"
cat /tmp/metadata.headers /tmp/metadata-before.json
```

cmp succeeds silently when duplicate responses match. Redirect/HEAD should
return 302 and Location: https://go.dev/doc/. Metadata should return 200 and JSON
with url and a valid RFC3339 created_at. These redirect commands inspect headers
without fetching the destination; curl -L would follow the link as a client.

## 5. Verify 400 and 404 errors

```bash
curl -i -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{}'
curl -i -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{"url":"ftp://example.com"}'
curl -i -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{'
curl -i http://localhost:8080/unknown
curl -i http://localhost:8080/api/v1/links/unknown
```

Expect 400 for missing URL, unsupported scheme, and malformed JSON; expect 404
for unknown redirect and metadata codes. Bodies contain an error field and do
not include a success code. Store-failure 500 behavior is verified by the tests.

## 6. Graceful shutdown and restart with the same records

```bash
kill -TERM "$SERVER_PID"
wait "$SERVER_PID"
/tmp/url-shortener -addr :8080 -base http://localhost:8080 \
  -store file -data ./data/grader-links.json -create-rate 0 \
  > /tmp/url-shortener-server.log 2>&1 &
SERVER_PID=$!
cat /tmp/url-shortener-server.log
```

SIGTERM starts the drain; wait waits for exit (normally status 0). The new process
loads the same snapshot before listening. After its startup banner appears:

```bash
curl -sS -o /tmp/metadata-after.json "http://localhost:8080/api/v1/links/$CODE"
cmp /tmp/metadata-before.json /tmp/metadata-after.json
curl -sS -o /tmp/shorten-after.json -X POST http://localhost:8080/api/shorten \
  -H 'Content-Type: application/json' -d '{"url":"https://go.dev/doc/"}'
cmp /tmp/shorten.json /tmp/shorten-after.json
curl -I "http://localhost:8080/$CODE"
```

Both cmp commands should succeed: code and created_at remain unchanged. Redirect
still returns 302. The automated tests additionally hold a request open to verify
shutdown actually drains it, and exercise the forced-close deadline.

## 7. Check the bonus create rate limit

Start a separate memory server on port 8081; it does not share the durable file:

```bash
/tmp/url-shortener -addr :8081 -base http://localhost:8081 -store memory \
  -create-rate 0.1 -create-burst 2 \
  > /tmp/url-shortener-rate.log 2>&1 &
RATE_PID=$!
cat /tmp/url-shortener-rate.log
```

After the startup banner, send three requests quickly:

```bash
for n in 1 2 3; do
  curl -sS -i -X POST http://localhost:8081/api/shorten \
    -H 'Content-Type: application/json' -d '{"url":"https://example.com/rate"}'
done
```

Expect 201, 201, then 429 with JSON error and Retry-After. The bucket refills one
token every 10 seconds; duplicate requests also consume tokens. Reads do not:

```bash
curl -i http://localhost:8081/api/v1/links/unknown
sleep 10
curl -i -X POST http://localhost:8081/api/shorten \
  -H 'Content-Type: application/json' -d '{"url":"https://example.com/rate"}'
```

The metadata request returns ordinary 404, not 429; the POST after refill should
return 201. Concurrent/refill tests provide deterministic verification.

## 8. Stop processes and review the submission

```bash
kill -INT "$RATE_PID"
wait "$RATE_PID"
kill -TERM "$SERVER_PID"
wait "$SERVER_PID"
```

SIGINT and SIGTERM both trigger graceful shutdown. Run these against the processes
started above in the same shell. The persisted snapshot remains for future runs.

Review README.md, CHECKLIST.md, and every Parts 1–4 decision in DECISIONS.md.
The selected bonuses are Part 5's LB → N apps → shared store proposal and Part 6's
shutdown/rate limit. Other bonus rows are left unchecked. Actual AI use is recorded
in DECISIONS.md separately from runtime grading requirements.
