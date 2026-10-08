package app

// DeployTx: deployments, steps, log, cluster.
// Owner: the deploy area (docs/go/spec/projects.md, docs/go/spec/swarm-worker.md).
type DeployTx interface{}

// Swarm drives Docker Swarm through the manager's socket (adapters/swarm).
type Swarm interface{}
