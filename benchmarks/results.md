# Load Test Results

k6 benchmarks against the semantic search API (`POST /api/v1/search`), running locally
via Docker Compose (Go API + Go worker + Python embedder sidecar + Postgres/pgvector +
Redis), seeded with the full `pubmed_corpus` dataset (exercise-science abstracts across
13 topics).

## Methodology note: rate limiter

The API enforces a 60 req/min-per-IP sliding-window rate limit (`internal/store/redis.go`,
keyed off `X-Forwarded-For`). Since k6 and the API share `localhost`, every VU originally
collapsed onto one IP bucket and got throttled well before the search pipeline was actually
stressed (>75% of requests came back `429` in the first runs). `benchmarks/load_test.js`
now sends a synthetic `X-Forwarded-For: 10.0.${__VU}.${__ITER}` header — unique per
*request*, not per VU — so the limiter never triggers and the results below reflect the
actual search pipeline (Postgres/pgvector + Redis cache + embedder), not the rate limiter.
The limiter itself is correct and by design; it's simply orthogonal to what this benchmark
measures and would need its own dedicated single-IP test.

## Results

| Scenario | VUs | Duration | Req/s | p50 | p95 | p99 | Error rate | Cache hit rate |
|---|---|---|---|---|---|---|---|---|
| `smoke` | 2 | 30s | 5.9 | 5.9ms | 217ms | — | 0.00% | 89.9% |
| `search_ramp` | 0→50 | 2m30s | 88.5 | 4.1ms | 72.1ms | 180.9ms | 0.00% | 100.0% |
| `full` (constant 10 + ramp 0→50 + health) | up to 62 | 2m | 131.5 | 3.9ms | 118.6ms | 278.2ms | 0.00% | 99.7% |
| `search_spike` | 0→200 | 1m30s | 218.8 | 5.1ms | 821.8ms | — | 0.00% | 99.98% |

All threshold checks passed in every scenario (`http_req_duration`, `error_rate`,
`cache_hit_rate`). Full command reference in `benchmarks/load_test.js`'s header comment:

```
k6 run benchmarks/load_test.js                          # smoke
k6 run -e SCENARIO=search_ramp benchmarks/load_test.js  # ramp to 50 VUs
k6 run -e SCENARIO=search_spike benchmarks/load_test.js # spike to 200 VUs
k6 run -e SCENARIO=full benchmarks/load_test.js         # constant + ramp + health
```

## Observations

- **Cache dominates at steady state.** Query cache hit rate sits at 99.7-100% in every
  sustained scenario (5-min TTL, small fixed query pool in the test script), so p50 latency
  (4-6ms) reflects a Redis lookup, not a cosine search + rerank.
- **p95/p99 track cache misses and embedder calls.** The gap between p50 (~4-6ms) and p95
  (72-822ms) is the cost of an actual cache-miss request: embed via the Python sidecar,
  pgvector HNSW search, evidence-aware rerank. This gets more visible as VU count rises —
  p95 goes from 72ms at 50 VUs (ramp) to 822ms at 200 VUs (spike), since more concurrent
  requests compete for the sidecar and the goroutine-handled DB pool at once.
- **No errors at up to 200 concurrent VUs.** Zero failed requests across all scenarios once
  the rate limiter was factored out — the pipeline holds up structurally at this scale.
  1M+ vectors or sustained (non-spike) 200-VU traffic aren't tested here and would be the
  next things to check before drawing conclusions past this corpus size.
