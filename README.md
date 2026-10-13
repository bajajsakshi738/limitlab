# LimitLab

A rate limiter for an HTTP API, written in Go.

Every API has to stop one client from using up capacity meant for everyone. The usual answer is a per-client limit ("10 requests a second, then 429"). That's easy on one server. It gets interesting once the API runs as several copies behind a load balancer, because each copy only sees part of the traffic.

This project starts with the easy version and works up to the hard one:

1. **One instance, limits kept in memory** (where it is now)
2. Postgres for keys and rules, plus a small dashboard
3. Three instances behind nginx, which breaks the limits: a 10 req/s key gets about 30 through
4. Shared limits in Redis with an atomic Lua script, and what happens when Redis goes down
5. Load tests, numbers, and a live deployment

## What works today

- `GET /api/data` needs an API key in the `Authorization: Bearer` header
- Two plans: **free** (10 req/s) and **pro** (100 req/s), using a token bucket
- Over the limit you get `429` with `Retry-After`
- Every limited response carries the rate limit headers from the current IETF draft ([draft-ietf-httpapi-ratelimit-headers-10](https://www.ietf.org/archive/id/draft-ietf-httpapi-ratelimit-headers-10.txt)):

```
RateLimit-Policy: "free";q=10;w=1
RateLimit: "free";r=7;t=1
```

`q` is the burst, `w` is how long an empty bucket takes to fill, `r` is what's left now and `t` is how many seconds until there's more.

## Running it

Needs Go 1.24 or newer.

```sh
# make a key (only its SHA-256 hash is stored, so copy it when it's printed)
go run ./cmd/keygen -plan free

# start the API on :8080
go run ./cmd/api

# in another terminal
curl -i -H "Authorization: Bearer <your key>" localhost:8080/api/data
```

To see the limit kick in, send a burst:

```sh
for i in $(seq 15); do
  curl -s -o /dev/null -w "%{http_code} " -H "Authorization: Bearer <your key>" localhost:8080/api/data
done
```

The first 10 come back `200` and the rest `429`, unless your machine is slow enough that a token refills in between.

Tests:

```sh
go test -race ./...
```

## Design notes

**Token bucket.** Each key has a bucket that holds up to `burst` tokens and refills at `rate` per second. A request takes one token or gets a 429. Tokens are decimals: rounding a refill down to whole tokens would quietly starve clients that call often (50 ms at 10/s is half a token, not zero). The refill is calculated when a request arrives, so nothing runs in the background per key.

**Keys.** A key looks like `ll_` + 32 random characters + a 6 character checksum. The checksum catches typos and junk before any lookup happens. It isn't a security feature, since anyone can compute it. Only a SHA-256 of the key is stored. A slow hash like bcrypt matters for guessable passwords, not for 190 bits of randomness.

**Small things that matter:**

- The clock is read while holding the bucket's lock. Read before it, two requests that arrive together can refill the same stretch of time twice. There's a test that fails if this is ever moved.
- The HTTP server has timeouts. Go's default server has none, so a client sending one header byte a minute could hold a connection forever.
- A key store error returns `503`, not `401`. The database being down isn't the client's fault.
- 401s are not rate limited. That would need limiting by IP, which this project leaves out on purpose.

## Layout

```
cmd/api        the service
cmd/keygen     creates keys
internal/
  apikey       key format, checksum, hashing
  limiter      token bucket math and the in-memory limiter
  keys         key lookup (a JSON file for now, Postgres next)
  plans        plan -> rule
  api          HTTP handlers and middleware
```
