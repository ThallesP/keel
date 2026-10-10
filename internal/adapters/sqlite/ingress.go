package sqlite

import (
	"github.com/ThallesP/keel/internal/adapters/sqlite/db"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Implements app.IngressTx.

func (t *tx) IngressHasVariable(nodeID, key string) (bool, error) {
	return t.q.IngressHasVariable(t.ctx, db.IngressHasVariableParams{NodeID: nodeID, Key: key})
}

func (t *tx) IngressOtherEndpoints(nodeID string) ([]app.OwnedEndpoint, error) {
	rows, err := t.q.IngressListOtherEndpoints(t.ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]app.OwnedEndpoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.OwnedEndpoint{
			Protocol:   domain.EndpointProtocol(r.Protocol),
			Domain:     str(r.Domain),
			PublicPort: intOr0(r.PublicPort),
			Owner:      r.Name,
		})
	}
	return out, nil
}

func (t *tx) IngressRoutes() ([]app.ProxyRoute, error) {
	rows, err := t.q.IngressListRoutes(t.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]app.ProxyRoute, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.ProxyRoute{
			NodeID:     r.NodeID,
			Protocol:   domain.EndpointProtocol(r.Protocol),
			Port:       int(r.Port),
			Domain:     str(r.Domain),
			PublicPort: intOr0(r.PublicPort),
		})
	}
	return out, nil
}

func (t *tx) IngressAnyEndpoint() (bool, error) {
	return t.q.IngressAnyEndpoint(t.ctx)
}

func (t *tx) IngressNodesWithDomain(name string) ([]string, error) {
	return t.q.IngressListNodesWithDomain(t.ctx, &name)
}
