# Design decisions

## Part 1

- **URL normalization:** Two URLs are treated as the same only when their complete
  strings match. I keep case, trailing slashes, fragments, and query order because
  changing them could merge links that have different meanings.
- **Idempotency storage:** Used two maps: code-to-record and URL-to-code. This
  lets me find a link in either direction without scanning every record. I check
  for an existing URL before generating a code, so duplicates keep their code
  and creation time.
- **Code generation:** Six random alphanumeric characters are generated with
  crypto/rand and added a weighted checksum as the seventh character. I chose
  randomness to avoid predictable counter codes and kept the checksum as a basic
  aid against typing mistakes. Lookup still uses the exact stored code; the
  checksum does not guarantee typo detection or prevent every wrong redirect.
- **Collision handling:** First checked whether a new code is already used and retry
  up to 128 times. I chose a limit so a failing generator cannot loop forever.
  If all attempts fail, ErrCodeCollision is returned without overwriting a record.
- **Mutex and lock boundaries:** I used RWMutex. Create holds a write lock through
  duplicate lookup, code allocation, and both map updates, so concurrent requests
  cannot allocate different codes for the same URL. Get uses a read lock, allowing
  several lookups at once. (Create & Get are store methods)
- **Package layout:** I kept startup and configuration in cmd/server, HTTP work
  in handler, validation and error wrapping in service, and record management in
  storage. domain holds shared types and errors. I chose these boundaries so I
  can change storage without changing the HTTP API.
- **Configuration and validation:** I used the required defaults: -addr :8080 and
  -base http://localhost:8080. Allowed a trailing slash in -base and add one
  separator when building short_url. Only absolute http/https URLs are accepted and
  no submitted URL is fetched.

### A note on code generation

In the project documentation it is mentioned that the same URL should return the same
and that indicates an algorithm which would generate the same code, but it is not
good with collisions, like if there is a unique way to generate the code from
URL, so there also will be a unique way to generate another code after a collision
then this algorithm treats collisions just like the nodes of a linked list and goes
to the end of the list on every collision.

So I stuck to the random code generation and duplex way to access URL records as
code-to-record and URL-to-code slices, so after a collision the new code is generated
randomly that reduces the probability of another collision.

## Part 2

- **Store interface:** Store is declared in main using the shared domain.Store
  interface. Its methods are Create(rawURL, generator), which returns a code or
  error, and Get(code), which returns a record or error. I share the contract in
  domain so the service can use it without importing main. Both storage backends
  and test fakes implement it, making storage replaceable and errors easy to test.
- **HTTP errors:** ErrInvalidURL maps to 400 and ErrNotFound to 404 using
  errors.Is after contextual %w wrapping. Other errors map to 500. API error bodies
  use {"error":"message"}; so clients get one consistent format without internal error
  details. store error details and full submitted URLs are not exposed.
  Each error branch returns immediately. Unsupported methods return 405 with Allow.
- **Specific validation errors:** I made ErrEmptyURL and ErrInvalidSchema wrap
  ErrInvalidURL. This keeps their specific meanings while letting one handler
  rule return 400 for all invalid URLs.
- **Metadata:** The code is extracted with PathValue and return exported fields
  tagged as url and created_at. I used UTC RFC3339 timestamps as it was mentioned
  in project documentation. A duplicate shorten request keeps the original
  timestamp because it refers to the same stored link.
- **Idempotency through Store:** I put allocation inside Create instead of making
  separate Exists and Save calls. This keeps duplicate checking and insertion
  together under one lock, including after changing the storage backend.
- **JSON responses:** I accept one JSON value per request to reject extra or
  malformed data. If writing a response fails after headers are sent,
  it would be logged as a generic message because sending another
  HTTP error would mix two responses.

## Part 3

- **Locking choice:** I chose RWMutex because I expect more redirects and metadata
  reads than new links. Readers can run together, while creates take an exclusive
  lock. I keep generation inside the write lock to make allocation atomic. I have
  not benchmarked RWMutex against Mutex, so this is a workload-based choice.
- **Timeouts:** I set ReadHeaderTimeout to 5s, ReadTimeout to 10s, WriteTimeout to
  10s, and IdleTimeout to 60s. They limit slow headers, request bodies, response
  writes, and idle keep-alive connections. I chose short read/write limits because
  the API handles small JSON requests, and a longer idle limit to allow connection reuse.
- **Memory limit:** I do not evict links or set a capacity limit for this project.
  Eviction would break old links and could give the same URL a different code.
  I accept growing memory use for the single-process assignment.
- **Benchmarks:** Benchmarked duplicate shortening, new URLs, and redirects through
  the HTTP handlers and router. I exclude setup from timing and reset the new-URL
  store every 1024 creates to keep benchmark memory bounded. I include request and
  recorder construction, so the results describe this test path rather than live
  network performance. The measured results are in README.md.
- **Profiling:** I couldn't see cpu profile results on my device. I tried the
  standard -cpuprofile command, but the run was terminated before I could inspect
  its output. I used allocation profiling instead to get a measured insight:
  request construction accounts for much of the benchmark's allocated memory.

## Part 4 — Persistence

- **Storage choice:** I chose file-backed JSON because it needs no database setup
  or extra modules and is easy to inspect and test. It supports one writer process
  per file path. Each create rewrites the full snapshot, so I use it for this
  assignment rather than a large distributed service.
- **File format and startup:** The snapshot contains version: 1 and a links map from
  code to URLRecord (url and created_at). The URL-to-code index is rebuilt on startup so
  two saved indexes cannot disagree. Unsupported versions, malformed
  records, and duplicate URLs are rejected to avoid silently losing or changing links.
- **Crash safety:** I prepare a new snapshot while holding the write lock. I write
  it to a temporary file in the same directory, sync and close it, rename it
  atomically, and sync the directory before returning success. I also sync newly
  created directories and their nearest existing parent. I chose this sequence
  to keep a complete previous or new snapshot after interruption and persist
  before HTTP 201. It requires a filesystem supporting rename and file/directory fsync.
- **Failed writes:** Before rename, I keep the previous file and maps unchanged.
  If rename succeeds but directory sync fails, I return an error and stop serving
  store reads/writes until restart because the committed state is uncertain.
  Restart loads the complete snapshot that survived. A client can retry the same
  URL without creating a different link if the failed request actually committed.
- **created_at:** The original UTC RFC3339 string is saved in JSON and loaded
  unchanged. It is not upgraded on duplicates, because it records the link's
  creation rather than its most recent request.
- **Idempotency after restart:** I rebuild both maps from the saved records and
  hold one write lock through lookup, allocation, persistence, and map publication.
  This preserves the same-URL rule during concurrent requests and after restart.
  Temporary-directory, reopen, HTTP, collision, and failure tests check this behavior.
- **Backend configuration:** I kept memory as the default for a simple run and
  use -store file with -data (default data/links.json) for persistence. I load
  storage before listening so a bad file causes a clear startup failure instead
  of starting with missing records. Temporary files use mode 0600 and newly
  created directories use 0700 to limit access to stored URLs.

## Part 5 — LB → N apps → shared store

```text
Proposed (not built): Clients → Load balancer → N Go replicas → Shared PostgreSQL
Implemented (today):  Clients → single process → MemoryStorage or file-backed JSON
```

- I would put several Go replicas behind a load balancer and
  keep link records in one shared store such as Redis or PostgreSQL. Any replica
  could handle a request without sticky sessions, because the records would not
  depend on a replica's local memory. Today's file backend cannot do this: it
  supports one writer process per file path, so it is not a shared store.
- I would use a code primary key and a unique URL constraint.
  Transactions would commit before 201; code collisions would retry, and concurrent
  inserts of the same URL would reuse the winning record. I would choose database
  constraints to enforce uniqueness across processes rather than relying on each
  replica's mutex. This is a database design I have not built.
- This would improve app capacity and availability but add database maintenance,
  network latency, and a shared failure dependency. I would use bounded connection
  pools, backups, and failover. If the database failed, I would return an error
  because isolated memory fallbacks could issue conflicting links.
- I would coordinate rate limits at a gateway or shared store because each
  replica's current bucket has its own budget. The rate limiter that exists in the
  code is a single in-process token bucket, so extra replicas would each get a
  separate budget. I have not added a distributed cache, so no stale-cache
  behavior is claimed.

## Part 6 — Graceful shutdown and create rate limiting

- **Shutdown:** signal.NotifyContext listens for SIGINT and SIGTERM, Server.Shutdown
  runs under a separate context. This stops new connections
  while allowing in-flight requests to finish. I chose a 10s default drain limit
  to avoid waiting forever; -shutdown-timeout can change it. At the deadline I
  close connections and exit with an error. SIGKILL cannot drain requests, and
  clients may need to retry if a write committed before their connection closed.
- **Shutdown tests:** I used a real listener and a blocked handler to check both
  successful draining and forced closure at the deadline. I also checked a built
  process with SIGTERM/SIGINT and verified that its saved links survived restart.
- **Rate limit:** I used one token bucket per process to keep memory use bounded
  without storing client IPs. It starts with 20 tokens and refills at 5 requests/s,
  allowing a small burst while limiting sustained creates. -create-rate and
  -create-burst configure it; rate 0 disables it. Only POST /api/shorten consumes
  tokens, including invalid and duplicate requests, so read traffic stays available.
  Rejections return JSON 429 and Retry-After rounded up to whole seconds.
- **Rate-limit tradeoff:** Clients share the process budget, and extra replicas
  add separate budgets. I accept this simple limit here; a shared deployment
  needs coordinated enforcement. Refill and concurrent tests check the behavior.

## AI assistance

I used AI assistance for requirement review, debugging, refactoring, tests,
benchmarks, and documentation. I used it to put TODO comments in code based on each
part tasks and it very helped in implementing features, meanwhile it refactored some
parts of my code. And also i asked AI to completely figure out what project documentation
actually wants and what should i do for it. It also helped me to complete .md
files like told it to check my decisions with project documentation and fix my
grammar too.

I used only Go's standard library and no external dependencies so there ain't no explain.
