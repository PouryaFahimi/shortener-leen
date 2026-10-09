# Project checklist

Parts 1–4 and the selected bonus tasks are checked against the implementation
and tests run on 2026-10-09. Profiling used an allocation profile after CPU
profiling was killed on this host; see README.md for the measured evidence.

## Part 1 — MVP

| Done | Pts | Requirement |
| :---: | :---: | --- |
| [x] | 4 | POST /api/shorten returns 201 with code and short_url |
| [x] | 3 | Same URL returns same code (test required) |
| [x] | 4 | GET /{code} returns 302 with correct Location |
| [x] | 3 | Unknown code gives 404; bad/missing URL gives 400 |
| [x] | 3 | URL validation and no server-side fetch |
| [x] | 2 | Codes 6–8 chars for new URLs; collision strategy for new codes only |
| [x] | 2 | -base flag used for short_url |
| [x] | 2 | httptest shorten/redirect; table tests for bad URL and unknown code |
| [x] | 2 | Concurrent duplicate shortening and go test -race ./... |

## Part 2 — API and errors

| Done | Pts | Requirement |
| :---: | :---: | --- |
| [x] | 5 | Metadata route 200/404 with correct JSON |
| [x] | 4 | ErrNotFound and ErrInvalidURL from store/domain |
| [x] | 4 | %w and errors.Is in HTTP mapping |
| [x] | 5 | Store interface and fake used in tests |
| [x] | 4 | Metadata and error-mapping tests |
| [x] | 3 | HTTP tests and README document Store-backed idempotency |

## Part 3 — Performance and measurement

| Done | Pts | Requirement |
| :---: | :---: | --- |
| [x] | 5 | Server timeouts configured |
| [x] | 5 | Locking matches behavior and DECISIONS.md |
| [x] | 5 | Shorten and redirect benchmarks |
| [x] | 5 | README benchmark lines and profiling insight |
| [x] | 5 | Tests and race detection remain green |

## Part 4 — Persistence

| Done | Pts | Requirement |
| :---: | :---: | --- |
| [x] | 6 | File-backed persistent Store |
| [x] | 5 | Startup load and index reconstruction |
| [x] | 5 | Create persisted before success response |
| [x] | 4 | Restart tests using temporary storage and HTTP |
| [x] | 3 | Config selects memory vs persistent store |
| [x] | 2 | Race detection clean with persistent store |

## Part 5 — Selected architecture bonus

| Done | Pts | Requirement |
| :---: | :---: | --- |
| [x] | 4 | DECISIONS.md: LB → N apps → shared transactional store |
| [ ] | 3 | CDN/edge caching for redirects (not attempted) |
| [ ] | 3 | Additional write-path scaling architecture (not claimed) |
| [ ] | 3 | Sharding/partitioning strategy (not attempted) |
| [ ] | 4 | Additional cache/load-test bonus code (not attempted) |

## Part 6 — Selected production bonuses

| Done | Pts | Requirement |
| :---: | :---: | --- |
| [x] | 3 | Graceful SIGINT/SIGTERM shutdown working and documented |
| [x] | 3 | Configurable create rate limit with 429/Retry-After |
| [ ] | 2 | Domain policy bonus (not claimed) |
| [ ] | 2 | Additional logging bonus (not claimed) |
| [ ] | 4 | Metrics/pprof endpoint or structured logging bonus (not attempted) |

Global measured statement coverage: **93.9%**; vet and normal/race tests passed.
A real-process check verified durable restart, unchanged code/timestamp,
SIGTERM/SIGINT exit, rate-limit rejection, and unaffected reads. The architecture
bonus is documentation only. Actual AI use is recorded in DECISIONS.md.
