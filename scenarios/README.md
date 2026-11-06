# Scenarios

Each profile combines an Elasticsearch topology, test data, and optional network
faults. Pick one to recreate a local environment, then use Kibana or another client
to investigate its behavior. One profile can support several workflows.

File names follow `<topology>__<degradation>[__<fixture>].yml`, and the `id` inside
is the same name with `__` replaced by `_`. Run one by name:

```bash
kibana-scenario-runner play 1remote__slow25s --kibana-dir /path/to/kibana
```

The scenario runs in the background. Run `kibana-scenario-runner stop` from any
working directory to stop it.

To make your own, copy [`1remote__slow25s.yml`](1remote__slow25s.yml) and pass its
path instead of a name.

| Scenario | Infrastructure | Good for |
| --- | --- | --- |
| [`1remote__slow25s`](1remote__slow25s.yml) | 1 remote, 25s latency | Slow CCS queries, reporting, Agent Builder mapping |
| [`1remote__coord300_remote3s`](1remote__coord300_remote3s.yml) | Coordinator 300ms, remote 3s | Duplicate API calls, saved objects, alerting forms, Synthetics settings |
| [`1remote__coord2s`](1remote__coord2s.yml) | Coordinator 2s, remote 500ms | Metrics/alerting rule forms, `/api/metrics/source` |
| [`1remote__bw50k__bulk50x300`](1remote__bw50k__bulk50x300.yml) | 50 KB/s link, bulk metadata | `_field_caps` over a narrow pipe, field pickers |
| [`local__fields200x900`](local__fields200x900.yml) | ~180k local fields, no fault | Field search at extreme cardinality, ES\|QL editor |
| [`local__rows500k`](local__rows500k.yml) | 500k documents, 8g heap, no fault | 100k-row results in Discover, browser memory, circuit breakers |
| [`fanout20__tail8s`](fanout20__tail8s.yml) | 20 aliases, #20 at 8s | Console ES\|QL autocomplete, CCS smoke tests |
| [`fanout20__tail3s__archive10`](fanout20__tail3s__archive10.yml) | 20 aliases, #20 at 3s, ES archive ×10 | Realistic archived data over CCS |
| [`fanout10__partial_slow15s`](fanout10__partial_slow15s.yml) | 10 aliases, #3 and #7 slow | Mixed latency across aliases |
| [`fanout100__latency500`](fanout100__latency500.yml) | 100 aliases, 500ms on all | Fan-out breadth, source enumeration, index resolution |
| [`fanout100__tail25s`](fanout100__tail25s.yml) | 100 aliases, #50 at 25s | Remote clusters UI, ResolveCluster refresh |
| [`fanout100__tail25s__bulk50`](fanout100__tail25s__bulk50.yml) | 100 aliases, one slow, 50 bulk streams | Large source lists, autocomplete under load |
| [`fanout100__resolve2000`](fanout100__resolve2000.yml) | 100 aliases, 2000 streams, no fault | Source-resolution sweep (baseline) |
| [`fanout200__resolve2000`](fanout200__resolve2000.yml) | 200 aliases, 2000 streams, no fault | Source-resolution sweep (midpoint) |
| [`fanout400__resolve2000`](fanout400__resolve2000.yml) | 400 aliases, 2000 streams, no fault | Source-resolution sweep (largest) |

## Notes

**`resolve2000`**: run 100, then 200, then 400 aliases and compare duration and
response size. Each run recreates 2,000 data streams, which takes a few minutes.

**`archive10`**: loads Kibana's `logstash_functional` archive on the remote and
writes 10 copies of `logstash-2015.09.22` into `logstash-expanded`. Needs a
bootstrapped Kibana checkout. The data is from September 2015, so pick that time
range, and query `archive-logs-*:logstash-expanded` to hit only the copies.

**`fanout100__tail25s__bulk50`**: compare a broad source pattern, which crosses the
slow alias,

```esql
FROM logs-*-*,logs-*,filebeat-*,serverless-logging-*:logs-* | LIMIT 10
```

with a local-only query, which does not:

```esql
FROM logs-degradation-default | LIMIT 10
```

`/internal/esql/autocomplete/sources/all` may still list remote sources in both
cases; check it in the browser's network tab.
