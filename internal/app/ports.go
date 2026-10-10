package app

import (
	"context"
	"errors"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

var ErrNoRow = errors.New("no row")

type Store interface {
	Read(ctx context.Context, fn func(Tx) error) error
	Write(ctx context.Context, fn func(Tx) error) error
}

type Tx interface {
	CoreTx
	AuthTx
	CanvasTx
	DeployTx
	ObservabilityTx
	IngressTx
}

type CoreTx interface {
	Project(id string) (domain.Project, error)
	Environment(id string) (domain.Environment, error)
	Node(id string) (domain.Node, error)
	Nodes(environmentID string) ([]domain.Node, error)
	AllNodes() ([]domain.Node, error)
	OrganizationOfEnvironment(environmentID string) (string, error)

	InsertNode(n domain.Node) error
	UpdateNode(n domain.Node) error
	DeleteNode(id string) error
	ReplaceEndpoints(nodeID string, eps []domain.Endpoint) error

	Setting(key string) (string, bool, error)
	SetSetting(key, value string) error
}

type Connections interface {
	DisconnectSession(sessionID string)
	DisconnectUser(userID string)
}

type Publisher interface {
	Publish(organizationID string, topics []string)
}

type Jobs interface {
	After(key string, delay time.Duration, fn func(context.Context))
	Every(name string, interval time.Duration, fn func(context.Context))
}
