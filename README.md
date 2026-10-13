# limitlab

A rate limiter for an API, in Go.

The idea: every client gets a limit (like 10 requests per second) and anything over that gets a `429 Too Many Requests`. Doing this on one server is easy. Doing it when the API runs as 3 copies behind a load balancer is the actual problem, because each copy only sees part of the traffic. That's where I'm going with this project.

Right now it's just one server with the limits kept in memory.

## What's there now

- `GET /api/data`, which needs an API key (`Authorization: Bearer <key>`)
- two plans: free = 10 req/s, pro = 100 req/s
- token bucket algorithm
- `429` + `Retry-After` when you go over
- rate limit headers on every response, following the IETF draft ([draft 10](https://www.ietf.org/archive/id/draft-ietf-httpapi-ratelimit-headers-10.txt)):


RateLimit-Policy: "free";q=10;w=1
RateLimit: "free";r=9;t=1


q = burst size, w = seconds to refill fully, r = requests left, t = seconds until you get more.

## Run it

Go 1.24+.

powershell
go run ./cmd/keygen -plan free      # prints a key, copy it (only the hash gets saved)
go run ./cmd/api                    # starts on :8080


Then in another terminal:

powershell
$KEY = "<your key>"
curl.exe -i -H "Authorization: Bearer $KEY" localhost:8080/api/data


To actually hit the limit:

powershell
1..15 | ForEach-Object { curl.exe -s -o NUL -w "%{http_code} " -H "Authorization: Bearer $KEY" localhost:8080/api/data }


You get 200s and then 429s. (On Mac/Linux it's `curl` and a normal `for` loop.)

Tests: `go test ./...`. CI also runs them with `-race`. I'm on Windows and `-race` needs a C compiler there, so I let GitHub Actions do that part.

## How it works / why

**Token bucket.** Every key gets a bucket with `burst` tokens. Each request takes one, and the bucket refills at `rate` per second. No token means 429. I don't run a timer to refill. When a request comes in, I work out how much should have refilled since the last one.

Tokens are floats, not ints. If you round, a client calling every 50ms at 10/s gets 0.5 tokens per call → rounded to 0 → it never gets through.

**API keys.** Format is `ll_` + 32 random chars + a 6 char checksum. The checksum is just CRC32, so it only catches typos/junk before doing a lookup. It's not security, since anyone can compute it. I only store the SHA-256 of the key, not the key itself. No bcrypt because the key is random (~190 bits), so there's nothing to brute force like with passwords.



**Other stuff:**
- server timeouts are set (Go's default server has none, so a slow client could hold connections forever)
- if the key store errors 503 is  returned, not 401, since it's not the client's fault
- 401s aren't rate limited. That would need IP-based limiting, which I left out for now.


