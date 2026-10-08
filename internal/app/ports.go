package app

import (
	"context"
	"errors"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// ErrNoRow: a Tx lookup found nothing. Use cases turn it into a domain error with the message
// the caller should see; it never reaches the transport on its own.
var ErrNoRow = errors.New("no row")

// Store runs transactions. Writes are serialized (SQLite has one writer), so a read-modify-write
// inside Write is safe. The Tx is bound to the ctx it was opened with.
type Store interface {
	Read(ctx context.Context, fn func(Tx) error) error
	Write(ctx context.Context, fn func(Tx) error) error
}

// Tx is everything a use case may do inside one transaction. Each area declares its slice in
// ports_<area>.go and implements it in adapters/sqlite/<area>.go.
type Tx interface {
	CoreTx
	AuthTx
	CanvasTx
	DeployTx
	ObservabilityTx
	IngressTx
}

// CoreTx: reads and writes every area shares (adapters/sqlite/core.go).
type CoreTx interface {
	Project(id string) (domain.Project, error)
	Environment(id string) (domain.Environment, error)
	// Node and Nodes include the node's endpoints.
	Node(id string) (domain.Node, error)
	Nodes(environmentID string) ([]domain.Node, error)
	AllNodes() ([]domain.Node, error)
	OrganizationOfEnvironment(environmentID string) (string, error)

	// InsertNode / UpdateNode write every column of the node except Endpoints.
	InsertNode(n domain.Node) error
	UpdateNode(n domain.Node) error
	DeleteNode(id string) error
	// ReplaceEndpoints makes the node's endpoints exactly eps, in order (ids are kept or made).
	ReplaceEndpoints(nodeID string, eps []domain.Endpoint) error

	Setting(key string) (string, bool, error)
	SetSetting(key, value string) error
}

// Publisher pushes invalidation topics to an organization's connected dashboards.
type Publisher interface {
	Publish(organizationID string, topics []string)
}

// Jobs replaces Convex's scheduler and crons, in memory.
type Jobs interface {
	// After runs fn once after delay. If a job with the same key is already pending, this call is
	// dropped (that is the debounce Convex nodes.observeScheduled was for).
	After(key string, delay time.Duration, fn func(context.Context))
	// Every runs fn every interval until serve stops.
	Every(name string, interval time.Duration, fn func(context.Context))
}
