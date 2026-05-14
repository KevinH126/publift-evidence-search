/**
 * k6 load test for the Semantic Search Engine.
 *
 * Run scenarios:
 *   k6 run benchmarks/load_test.js                          # default (smoke)
 *   k6 run -e SCENARIO=search_ramp benchmarks/load_test.js  # ramp to 50 VUs
 *   k6 run -e SCENARIO=search_spike benchmarks/load_test.js # spike to 200 VUs
 *   k6 run -e SCENARIO=full benchmarks/load_test.js         # all scenarios
 */

import http from 'k6/http';
import { check, sleep, group } from 'k6';
import { Rate, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// Custom metrics
const errorRate   = new Rate('error_rate');
const cacheHitRate = new Rate('cache_hit_rate');
const searchP95   = new Trend('search_p95', true);

// Sample queries — exercise-science questions, diverse enough to stress the embedding cache
const QUERIES = [
  'does training to failure matter for hypertrophy',
  'optimal protein intake for muscle growth',
  'how many sets per muscle group per week',
  'full body vs split routine for strength',
  'effect of rest interval length on hypertrophy',
  'is progressive overload necessary for gains',
  'caffeine and resistance training performance',
  'creatine monohydrate loading protocol',
  'training frequency for natural lifters',
  'eccentric vs concentric muscle damage',
];

const scenario = __ENV.SCENARIO || 'smoke';

export const options = (() => {
  switch (scenario) {
    case 'search_ramp':
      return {
        scenarios: {
          ramp: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
              { duration: '30s', target: 10  },
              { duration: '60s', target: 50  },
              { duration: '30s', target: 50  },
              { duration: '30s', target: 0   },
            ],
          },
        },
        thresholds: {
          http_req_duration: ['p(95)<500', 'p(99)<1000'],
          error_rate:        ['rate<0.01'],
        },
      };

    case 'search_spike':
      return {
        scenarios: {
          spike: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
              { duration: '15s', target: 10  },
              { duration: '15s', target: 200 },
              { duration: '30s', target: 200 },
              { duration: '15s', target: 10  },
              { duration: '15s', target: 0   },
            ],
          },
        },
        thresholds: {
          http_req_duration: ['p(95)<2000'],
          error_rate:        ['rate<0.05'],
        },
      };

    case 'full':
      return {
        scenarios: {
          constant_search: {
            executor: 'constant-vus',
            vus: 10,
            duration: '60s',
            startTime: '0s',
          },
          ramp_search: {
            executor: 'ramping-vus',
            startVUs: 0,
            stages: [
              { duration: '30s', target: 50 },
              { duration: '60s', target: 50 },
              { duration: '30s', target: 0  },
            ],
            startTime: '0s',
          },
          health_checks: {
            executor: 'constant-vus',
            vus: 2,
            duration: '120s',
            exec: 'healthScenario',
          },
        },
        thresholds: {
          http_req_duration: ['p(50)<100', 'p(95)<500', 'p(99)<1000'],
          error_rate:        ['rate<0.01'],
          cache_hit_rate:    ['rate>0.3'],
        },
      };

    default: // smoke
      return {
        scenarios: {
          smoke: {
            executor: 'constant-vus',
            vus: 2,
            duration: '30s',
          },
        },
        thresholds: {
          http_req_duration: ['p(95)<1000'],
          error_rate:        ['rate<0.01'],
        },
      };
  }
})();

// ── Main scenario: semantic search ──
export default function () {
  const query = QUERIES[Math.floor(Math.random() * QUERIES.length)];
  const topK  = Math.random() < 0.5 ? 5 : 10;

  group('semantic_search', () => {
    const res = http.post(
      `${BASE_URL}/api/v1/search`,
      JSON.stringify({ query, top_k: topK }),
      { headers: { 'Content-Type': 'application/json' } },
    );

    const ok = check(res, {
      'status 200':          r => r.status === 200,
      'has results field':   r => JSON.parse(r.body).results !== undefined,
      'latency field present': r => JSON.parse(r.body).latency !== undefined,
    });

    errorRate.add(!ok);
    searchP95.add(res.timings.duration);

    if (res.status === 200) {
      const body = JSON.parse(res.body);
      cacheHitRate.add(body.cached === true);
    }
  });

  sleep(0.1 + Math.random() * 0.4); // 100–500ms think time
}

// ── Health check scenario ──
export function healthScenario() {
  const res = http.get(`${BASE_URL}/api/v1/health`);
  check(res, {
    'health 200': r => r.status === 200,
    'all healthy': r => {
      const b = JSON.parse(r.body);
      return b.api === 'healthy' && b.postgres === 'healthy' && b.redis === 'healthy';
    },
  });
  sleep(5);
}

// ── Mixed scenario (upload + search) ──
export function mixedScenario() {
  group('keyword_search', () => {
    const query = QUERIES[Math.floor(Math.random() * QUERIES.length)];
    const res = http.post(
      `${BASE_URL}/api/v1/search/keyword`,
      JSON.stringify({ query, top_k: 5 }),
      { headers: { 'Content-Type': 'application/json' } },
    );
    check(res, { 'keyword search 200': r => r.status === 200 });
    errorRate.add(res.status !== 200);
  });

  group('list_studies', () => {
    const res = http.get(`${BASE_URL}/api/v1/studies`);
    check(res, { 'list studies 200': r => r.status === 200 });
  });

  sleep(0.5);
}
