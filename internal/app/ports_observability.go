package app

// ObservabilityTx: log sinks, Axiom sign-in state, OTLP keys.
// Owner: the observability area (docs/go/spec/observability.md).
type ObservabilityTx interface{}

// Axiom is Axiom's HTTP APIs: OAuth, datasets, queries, ingest (adapters/axiom).
type Axiom interface{}

// LogReader reads container logs from Docker (the default sink: `docker service logs` on the
// manager). Implemented by adapters/swarm/logs.go.
type LogReader interface{}
