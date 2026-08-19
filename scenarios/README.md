# Scenario catalog

Each YAML file contains exactly one **infrastructure profile**: topology, fixtures,
and network faults. One profile can support many Kibana workflows — pick a row
below, run the profile, then exercise any listed use case.

File names follow `<topology>__<degradation>[__<fixture>].yml`. A degradation can
be a network fault or a deliberately large fixture. The `id` inside the file uses
single underscores (used for `.runtime/<id>/`).
The runner enforces this relationship: the `id` must equal the filename stem with
each `__` replaced by `_`.

## Naming

| Segment     | Meaning                       | Examples                                              |
| ----------- | ----------------------------- | ----------------------------------------------------- |
| Topology    | Remote layout                 | `1remote`, `fanout20`, `fanout100`, `local`           |
| Degradation | Fault or metadata load        | `slow25s`, `tail8s`, `coord2s`, `resolve2000`         |
| Fixture     | Additional shape, if relevant | `bulk50`, `bulk50x300`                                |

## Profiles

| File                                                               | Infrastructure                      | Example use cases                                                             |
| ------------------------------------------------------------------ | ----------------------------------- | ----------------------------------------------------------------------------- |
| [`1remote__slow25s.yml`](1remote__slow25s.yml)                     | 1 remote, 25s latency               | Slow CCS queries, reporting, Agent Builder mapping, template for new profiles |
| [`1remote__coord300_remote3s.yml`](1remote__coord300_remote3s.yml) | Coord 300ms, remote 3s              | Duplicate API detection, saved objects, alerting forms, Synthetics settings   |
| [`1remote__coord2s.yml`](1remote__coord2s.yml)                     | Coord 2s, remote 500ms              | Metrics/alerting rule forms, `/api/metrics/source` under load                 |
| [`1remote__bw50k__bulk50x300.yml`](1remote__bw50k__bulk50x300.yml) | 50 KB/s link, bulk metadata         | `_field_caps` over narrow pipe, field pickers (Lens, Maps, rules)             |
| [`local__fields200x900.yml`](local__fields200x900.yml)             | ~180k local fields, no fault        | Field search at extreme cardinality, unified search, ES\|QL editor            |
| [`local__rows500k.yml`](local__rows500k.yml)                       | 500k documents, 8g heap, no fault   | 100k-row results in Discover, browser memory, circuit breakers                |
| [`fanout20__tail8s.yml`](fanout20__tail8s.yml)                     | 20 aliases, #20 at 8s               | Console ES\|QL autocomplete, CCS smoke tests                                  |
| [`fanout10__partial_slow15s.yml`](fanout10__partial_slow15s.yml)   | 10 aliases, #3 and #7 slow          | Mixed latency where only some aliases are slow                                |
| [`fanout100__latency500.yml`](fanout100__latency500.yml)           | 100 aliases, 500ms all              | CCS fan-out breadth, source enumeration, index resolution                     |
| [`fanout100__tail25s.yml`](fanout100__tail25s.yml)                 | 100 aliases, #50 at 25s             | Remote clusters UI, ResolveCluster refresh                                    |
| [`fanout100__tail25s__bulk50.yml`](fanout100__tail25s__bulk50.yml) | 100 aliases, tail + 50 bulk streams | Large source lists with one slow region, autocomplete under load              |
| [`fanout100__resolve2000.yml`](fanout100__resolve2000.yml)         | 100 aliases, 2000 streams, no fault | Source-resolution sweep baseline                                              |
| [`fanout200__resolve2000.yml`](fanout200__resolve2000.yml)         | 200 aliases, 2000 streams, no fault | Source-resolution sweep midpoint                                              |
| [`fanout400__resolve2000.yml`](fanout400__resolve2000.yml)         | 400 aliases, 2000 streams, no fault | Largest source-resolution sweep point                                         |

The three `resolve2000` profiles form an experiment rather than three guaranteed
latency levels. Start with 100 aliases and compare duration and response size before
moving to 200 and 400. Each run recreates 2,000 data streams and can take several
minutes.

## Quick start

```bash
./elasticsearch-degradation-lab validate scenarios/1remote__slow25s.yml
./elasticsearch-degradation-lab up scenarios/1remote__slow25s.yml --kibana-dir /path/to/kibana
```
