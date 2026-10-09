# Design decisions

## Part 1

- **URL normalization:** Equality uses the exact submitted string. Case,
  trailing slashes, fragments, and query ordering are preserved. This avoids
  silently merging URLs whose meaning may differ; http/https and a host are required.
- **Idempotency storage:** MemoryStorage maintains code-to-record and URL-to-code
  indexes. Create checks duplicates and updates both indexes under one write lock.
  Duplicate calls skip code generation and preserve the original timestamp.
- **Code generation:** New URLs receive six random alphanumeric characters drawn with
  crypto/rand.Int from a 62-character alphabet plus the existing weighted checksum
  suffix, producing seven characters. The checksum is not a guarantee
  of detecting every transcription error. It prevents redirection to a different
  URL by changing some characters of code randomly.
- **Collision handling:** Create checks candidates while holding its write lock,
  retries up to 128 times, then wraps ErrCodeCollision. Failed generation/exhaustion
  does not insert records or overwrite occupied codes. Deterministic test generators
  exercise retries, exhaustion, and failure without relying on random collisions.
- **Mutex and lock boundaries:** Create (store method) takes Lock across both indexes,
  allocation, and timestamp insertion. Get uses RLock and returns a record value.
  This makes duplicate allocation atomic and permits concurrent reads.
- **Package layout:** cmd/server composes configuration, Store, service, handler,
  and server. domain defines shared records/errors/contracts. service validates
  and wraps errors; storage owns atomic allocation and records; handler owns HTTP.
- **Configuration and responses:** -addr is :8080 and -base is http://localhost:8080
  by default. Flags are parsed before use; the handler removes trailing
  slashes and appends one separator. Shorten returns 201 with code and short_url;
  redirects accept GET and HEAD on one code segment. No submitted URL is fetched.

### A note on code generation

In the project documentation it is mentioned that by a same URL a same code should
be generated and that indicates an algorithm which would generate same code, but it
is not good with collisions, like if there is a unique way to generate the code from
URL, so there also will be a unique way to generate another code after a collision
then this algorithm treats collisions just like the nodes of a linkedlist and goes
to end of the list on every collision.

So I sticked to the random code generation and duplex way to access URL records as
code-to-record and URL-to-code slices, so after a collision the new code is generated
randomly that reduces happening another collision probability.

## Part 2

- **Store interface location and methods:** Store is declared at the main consumer
  boundary by embedding the shared domain.Store contract. Create(rawURL, generator)
  returns code/error; Get(code) returns URLRecord/error. MemoryStorage and test fakes
  satisfy this contract; the service accepts the shared interface so it does not
  import main. The handler also depends on a small LinkService interface.
- **Errors to HTTP:** ErrInvalidURL maps to 400 and ErrNotFound to 404 using
  errors.Is after contextual %w wrapping. Other errors map to 500. API error bodies
  use {"error":"message"}; store error details and full submitted URLs are not exposed.
  Each error branch returns immediately. Unsupported methods return 405 with Allow.
- **Existing sentinel errors:** ErrEmptyURL and ErrInvalidSchema wrap ErrInvalidURL,
  preserving specific identities while giving all invalid input the required 400 mapping.
- **Metadata representation and routing:** Go wildcard routes extract code with
  PathValue. Exported URL and CreatedAt fields encode as url and created_at.
  Creation timestamps are UTC RFC3339 strings assigned once on successful insertion.
- **Idempotency through Store:** Allocation is a Store operation rather than
  separate Exists/Save calls. HTTP, service, and concurrent tests verify code reuse;
  storage tests explicitly check unchanged historical timestamps on duplicates.
- **JSON handling:** Shorten accepts exactly one JSON value; malformed input or
  trailing values are rejected. Once response headers have been sent, write failures
  are logged generically rather than attempting to send a second status/body.

## Part 3

- **Locking choice:** RWMutex fits the expected many-redirects/fewer-creates workload.
  Get can run concurrently; Create requires an exclusive lock. Code generation
  happens inside the write lock for straightforward correctness. Actual contention
  has not been benchmarked against Mutex; do not claim this choice was proven faster.
- **Timeouts and slow clients:** ReadHeaderTimeout is 5 seconds to limit slow
  headers. ReadTimeout is 10 seconds for reading the request including its body.
  WriteTimeout is 10 seconds to bound slow response writes. IdleTimeout is 60 seconds
  for idle keep-alive connections. This small JSON API has no long-lived streaming
  responses; these defaults should be reassessed for a different workload.
- **Eviction cap:** The memory store is intentionally unbounded for the single-process
  assignment. Eviction would invalidate old links and weaken same-URL idempotency;
  capacity limits are deferred rather than silently deleting data. Part 4 will add
  durability, not automatically a memory or capacity policy.
- **Measurement method:** Handler benchmarks use httptest through the mux and
  include request/recorder construction. Duplicate shortening and redirects are
  seeded outside timing. New URL creation resets stores every 1024 entries outside
  timing to keep memory bounded. Actual measurements are in README.md.
- **Profiling:** CPU profiling was killed immediately in two attempts on this host.
  Successful pprof alloc_space output shows benchmark request construction dominates
  sampled allocated bytes. This is an allocation insight about the harness, not a
  CPU or production-throughput conclusion. See README.md for values and commands.

## Part 4 — Persistence

- **Storage choice:** File-backed JSON uses encoding/json and os with no extra
  modules. It fits a single-process assignment and is easy to inspect and test;
  full-snapshot writes and startup loading are deliberately unsuitable for millions
  of links or multiple writer processes. One active writer per path is required.
- **Schema and migrations:** The snapshot contains version: 1 and a links map from
  code to URLRecord (url and created_at). URL-to-code is derived on startup, avoiding
  a separately persisted index that could drift. Unknown fields/versions, malformed
  input, bad codes/URLs/timestamps, and duplicate URLs fail startup. There is no ORM
  or SQL migration; future format changes need an explicit version migration.
- **Crash safety and atomicity:** Create holds the file-store write lock, clones both
  indexes, allocates using the memory-store collision rules, then writes a complete
  candidate JSON snapshot. The temporary file is in the destination directory, mode
  0600; it is synced and closed before atomic rename. The directory is synced before
  the candidate becomes visible or Create returns success. Newly created parent
  directories and their nearest existing ancestor are also synced on startup.
  Guarantees assume a filesystem honoring atomic rename and file/directory fsync.
- **Failure recovery:** Before rename, failures leave the previous snapshot/indexes
  untouched. After rename, a sync error means an ambiguous commit: the process
  fails closed for reads/writes until restart instead of serving stale memory.
  Restart loads a complete surviving snapshot. An error response may correspond
  to a committed write; same-URL retry safely recovers the original link. Temporary
  files left by abrupt termination are ignored as uncommitted candidates.
- **created_at:** UTC RFC3339 strings round-trip unchanged in JSON. Duplicate
  creation returns before allocating/writing and never changes timestamps.
- **Idempotency after restart:** Startup rebuilds both indexes and rejects duplicate
  URLs. One write lock spans duplicate lookup, allocation, persistence, and index
  publication. Reopen, HTTP restart, timestamp, collision, failed-write, and
  concurrent durable-store tests verify these contracts.
- **Configuration:** -store memory is the default; -store file selects persistence
  at -data (default data/links.json). The backend loads before listen; startup and
  listen errors return a nonzero exit. There are no long-lived file handles to close.

## Part 5 — LB → N apps → shared store

```text
Clients → Load balancer → N Go replicas → Shared PostgreSQL
```

- The load balancer distributes requests among replicas. Authoritative link state
  lives in the shared store, so a request can reach any replica without sticky
  sessions. The current memory/file backends would be replaced by a database Store;
  separate per-replica files or shared-file concurrent writers are not this design.
- PostgreSQL would store code as a primary key, URL under a unique constraint with
  equality matching the exact-string normalization policy, and creation timestamp.
  A transaction finds/reuses an existing URL or inserts a new code and commits
  before 201. Unique-code conflicts retry allocation; concurrent URL conflicts
  resolve to the winning row, preserving idempotency across replicas.
- The load balancer improves capacity and app availability, but the shared database
  adds operational cost, network latency, and a common availability dependency.
  Use bounded connection pools, backups, and database failover. During database
  failure, return an error rather than falling back to isolated memory stores that
  could issue incompatible codes. Database reads define consistency; no distributed
  cache or stale-read policy is implemented or claimed here.
- Each replica's local rate bucket has its own budget; a fleet-wide limit would be
  enforced at the gateway or via coordinated shared storage. Replica count alone
  does not produce a global limit. This section is an architecture proposal, not
  an implemented distributed backend or load-balancer deployment.

## Part 6 — Graceful shutdown and create rate limiting

- **Shutdown drain:** signal.NotifyContext listens for SIGINT and SIGTERM. A
  separate shutdown context allows in-flight work to continue after the signal;
  Server.Shutdown stops listening and drains active requests for up to
  -shutdown-timeout (10s default). Completed file writes are committed before
  responses. At the deadline, Server.Close forces connections closed and the
  process exits with an error. A client may not receive a committed response;
  retrying the same URL recovers the code. SIGKILL cannot drain requests.
- **Verification:** Tests use a real listener and a blocked handler to prove the
  drain waits for in-flight completion and the timeout forces closure. A real
  binary was also exercised with SIGTERM/SIGINT and reopened durable records.
- **Rate limit:** A process-wide mutex-protected token bucket starts full at 20
  tokens, refilling at 5 requests/second. -create-rate and -create-burst configure
  it; rate 0 disables it. Positive burst, finite/nonnegative rate, and positive
  shutdown duration are validated. Rejection is JSON 429 with Retry-After rounded
  up to whole seconds. Only POST /api/shorten consumes tokens, including invalid
  and duplicate submissions. Reads and unsupported methods are unaffected.
- **Tradeoffs:** A global bucket has bounded memory and no proxy/IP trust issue,
  but unrelated clients share a budget and each replica gets a separate budget.
  A distributed deployment needs gateway/shared enforcement. Refill and concurrent
  admission tests check limits deterministically without timing-dependent sleeps.
- **Blocklist rules:** Only existing http/https/host validation is applied. No
  domain blocklist/allowlist bonus is claimed.
- **Observability:** Startup/listen/shutdown errors and generic response-write
  failures are logged. There is no request URL logging or metrics endpoint; full
  submitted URLs and query strings are not logged. Extra metrics/logging bonuses
  are not claimed.

## AI assistance

AI was used, at the user's request, to implement the Store refactor, related fixes,
tests/benchmarks/timeouts, implement file persistence, graceful shutdown and rate
limiting, review existing work, and update documentation. Earlier
assistance included requirement review and metadata-route debugging. The project
brief restricts AI to search/consultation; this entry records the actual use rather
than claiming the generated implementation was written without assistance.

<!-- No external modules are currently used. Add ## Dependencies if modules other
than an allowed web framework/router or GORM plus its SQL driver are introduced.
List import path, purpose, and why stdlib/the allowed stack was insufficient. -->
