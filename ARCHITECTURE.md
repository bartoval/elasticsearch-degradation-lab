# Architecture

The Elasticsearch degradation lab is a local orchestration tool for building
controlled Elasticsearch failure conditions for client investigations. A scenario
describes the topology, test data, and network faults; the runner turns that
description into local processes and proxy routes.

Kibana is the primary client but remains outside the runner's lifecycle. The runner
uses the Kibana checkout only to launch matching Elasticsearch snapshots through
`scripts/es.js`; it does not start or modify Kibana itself. Other clients can use
the same local Elasticsearch endpoints.

## System overview

```text
                         control and setup

Scenario YAML ──► CLI ──► Runner ─────► Toxiproxy API
                              ├────────► coordinator Elasticsearch
                              └────────► optional remote Elasticsearch

                         runtime traffic

Kibana ──HTTP──► coordinator proxy ──HTTP──► coordinator Elasticsearch
                                                    │
                                                    │ CCS transport
                                                    ▼
                                           per-alias proxies
                                                    │
                                                    ▼
                                         optional remote Elasticsearch
```

The control path prepares and configures the environment. The runtime path carries
the traffic being observed. Keeping the paths separate allows the runner to finish
setup against a healthy topology and activate degradation only when the environment
is ready.

## Main components

- **CLI** loads a scenario and exposes `validate` and `up`. YAML decoding is strict,
  so unknown fields and unsupported values fail before any process is started.
- **Runner** coordinates startup order, readiness, fixture creation, fault
  activation, process monitoring, and shutdown.
- **Elasticsearch integration** starts isolated single-node snapshots using
  Kibana's `scripts/es.js`, configures CCS aliases, and sends authenticated setup
  requests for fixtures and health checks. Fixtures come in two kinds, because the
  cost being reproduced differs: some create mappings only, which is what index
  resolution and field capabilities read, while others index documents, which is
  what a query has to return.
- **Toxiproxy integration** creates the network routes and applies latency or
  bandwidth toxics through its local HTTP API.
- **Process manager** owns the child processes and their process groups, allowing a
  signal or startup failure to tear down the complete environment.

## Environment creation

The `up` command builds a scenario in this order:

1. Load and validate the YAML scenario. References from fixtures and faults must
   resolve to the coordinator or the configured remote.
2. Download and verify the Toxiproxy binary when it is not cached, then start its
   local API and wait for it to become ready.
3. Create the coordinator HTTP proxy and, for a CCS topology, one transport proxy
   for every logical remote alias.
4. Start the optional remote Elasticsearch node, followed by the coordinator. Both
   use the snapshot version selected by the Kibana checkout and isolated runtime
   directories, and both receive whatever heap and extra settings the scenario
   declared, so that a limit under test never depends on which node answered.
5. Poll authenticated cluster health until each Elasticsearch node is ready.
6. Register every CCS alias on the coordinator in proxy mode. Each alias points to
   its own Toxiproxy port, although all aliases may lead to the same physical remote.
   The runner waits until `_remote/info` reports every alias as connected.
7. Create the declared fixtures directly on their target Elasticsearch node.
8. Apply the declared network faults only after topology and data setup have
   completed successfully, then report the environment as ready.

If any step fails, the runner stops everything that was already started instead of
leaving a partially built scenario active.

## Proxy and fault mechanisms

Kibana uses its normal Elasticsearch endpoint on port `9200`. Toxiproxy listens on
that port and forwards requests to the coordinator's direct HTTP endpoint on
`9210`. A fault targeting `coordinator` therefore affects the same requests Kibana
makes during normal use, including non-CCS requests.

For CCS, the coordinator connects to logical remote aliases through transport
proxies, each on its own port from a reserved range, all forwarding to the physical
remote transport endpoint on `9301`. This indirection provides two useful
mechanisms:

- many logical clusters can be simulated with one physical remote Elasticsearch
  process;
- a fault can target every alias or one alias selected by position, which models a
  single slow region inside a wider fan-out.

Faults are Toxiproxy toxics applied to downstream traffic, from Elasticsearch back
toward the caller. A latency fault adds a fixed delay with optional jitter. A
bandwidth fault caps the response rate per connection, making the delay proportional
to the amount of transport data crossing the proxy. Toxiproxy exposes per-proxy byte
counters so that observed timing can be related to actual traffic.

## Runtime state and lifecycle

Each scenario owns a directory under `.runtime/<scenario-id>/` containing isolated
Elasticsearch data and component logs. Data is reset on every `up`, while downloaded
Toxiproxy binaries and the Elasticsearch snapshot cache are reused.

After startup, the runner remains active and monitors Toxiproxy and every
Elasticsearch launcher. `Ctrl+C` or `SIGTERM` cancels the shared run context and
starts an orderly shutdown. An unexpected component exit invalidates the scenario,
reports the relevant log, and also triggers cleanup. Elasticsearch nodes stop before
Toxiproxy so that no cluster is left pointing at a proxy that has already vanished.

## Deliberate boundaries

- The runner supports zero or one physical remote cluster. The number of logical CCS
  aliases is bounded by the port range reserved for their proxies, and a scenario
  that exceeds it is rejected with the limit named.
- Kibana is started, configured, and stopped independently by the operator.
- All managed services listen locally; the tool is intended for development and
  reproducible investigation, not deployment.
- Scenarios describe infrastructure and degradation, while the Kibana workflow and
  measurements remain the operator's responsibility.
