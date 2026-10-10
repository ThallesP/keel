package app

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/ThallesP/keel/internal/domain"
)

type ExposeInput struct {
	Protocol   domain.EndpointProtocol
	Port       *float64
	Domain     *string
	PublicPort *float64
}

type UnexposeInput struct {
	Protocol   domain.EndpointProtocol
	Domain     *string
	PublicPort *float64
}

func (a *App) Expose(ctx context.Context, actor domain.Actor, nodeID string, in ExposeInput) (domain.Endpoint, error) {
	var out domain.Endpoint
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		node := scope.Node
		if node.Desired == nil || !node.Type.Deployable() {
			return domain.Invalid("Only services, databases and caches can be exposed")
		}
		if domain.EngineOf(node.Desired.Image) == domain.EngineRedis {
			has, err := tx.IngressHasVariable(node.ID, "REDIS_PASSWORD")
			if err != nil {
				return err
			}
			deployed := node.DeployedRevision != nil && *node.DeployedRevision == node.Desired.Revision
			if !has || node.Dirty || !deployed {
				return domain.Invalid("Ship this Redis first: its password takes effect on the next Ship")
			}
		}
		protocol := in.Protocol
		if protocol == "" {
			protocol = domain.ProtocolTCP
			if node.Type == domain.NodeService {
				protocol = domain.ProtocolHTTP
			}
		}
		port, err := domain.PortNumber(in.Port)
		if err != nil {
			return err
		}
		port = cmp.Or(port, node.Desired.Port)
		if port == nil {
			return domain.Invalid("Set the service's port first")
		}
		ip := a.Config.PublicIP
		if ip == "" {
			return domain.E(domain.CodeUnavailable, "Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP")
		}
		own := node.Endpoints
		others, err := tx.IngressOtherEndpoints(node.ID)
		if err != nil {
			return err
		}

		wanted := domain.Endpoint{Protocol: protocol, Port: *port}
		if protocol == domain.ProtocolHTTP {
			if in.PublicPort != nil {
				return domain.Invalid("HTTP is always served on 80 and 443")
			}
			name := domain.DefaultDomain(node.ID, node.Name, ip)
			if in.Domain != nil {
				if name, err = domain.ValidDomain(*in.Domain); err != nil {
					return err
				}
			}
			for _, o := range others {
				if o.Protocol == domain.ProtocolHTTP && o.Domain == name {
					return domain.Conflict("%s is already used by %s", name, o.Owner)
				}
			}
			wanted.Domain = name
		} else {
			if in.Domain != nil {
				return domain.Invalid("Only HTTP endpoints have a domain")
			}
			public, err := pickPublicPort(protocol, *port, in.PublicPort, own, others)
			if err != nil {
				return err
			}
			wanted.PublicPort = &public
		}

		key := wanted.Key()
		if i := slices.IndexFunc(own, func(e domain.Endpoint) bool { return e.Key() == key && e.Port == wanted.Port }); i >= 0 {
			out = own[i]
			return nil
		}
		rest := slices.DeleteFunc(slices.Clone(own), func(e domain.Endpoint) bool { return e.Key() == key })
		if len(rest) >= 10 {
			return domain.Invalid("At most 10 endpoints per node")
		}
		wanted.NodeID = node.ID
		wanted.PinnedPort = node.Desired.Port == nil || *node.Desired.Port != wanted.Port
		wanted.Status = domain.EndpointStatus{State: domain.EndpointStarting, At: a.Now()}
		if err := tx.ReplaceEndpoints(node.ID, append(rest, wanted)); err != nil {
			return err
		}
		ch.Environment(scope.Org, scope.Environment.ID)
		ch.AfterCommit(a.ScheduleProxySync)
		out = wanted
		return nil
	})
	return out, err
}

func (a *App) Unexpose(ctx context.Context, actor domain.Actor, nodeID string, in UnexposeInput) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, err := requireNode(tx, actor, nodeID)
		if err != nil {
			return err
		}
		all := in.Protocol == "" && in.Domain == nil && in.PublicPort == nil
		named := in.PublicPort != nil
		if in.Protocol == domain.ProtocolHTTP {
			named = in.Domain != nil
		}
		if !all && (in.Protocol == "" || !named) {
			return domain.Invalid("Name the endpoint: protocol and domain (http) or public port")
		}
		name := ""
		if in.Domain != nil && *in.Domain != "" {
			if name, err = domain.ValidDomain(*in.Domain); err != nil {
				return err
			}
		}
		node := scope.Node
		keep := slices.DeleteFunc(slices.Clone(node.Endpoints), func(e domain.Endpoint) bool {
			switch {
			case all:
				return true
			case e.Protocol != in.Protocol:
				return false
			case e.Protocol == domain.ProtocolHTTP:
				return e.Domain == name
			}
			return float64(*e.PublicPort) == *in.PublicPort
		})
		if len(keep) == len(node.Endpoints) {
			return nil
		}
		if err := tx.ReplaceEndpoints(node.ID, keep); err != nil {
			return err
		}
		ch.Environment(scope.Org, scope.Environment.ID)
		ch.AfterCommit(a.ScheduleProxySync)
		return nil
	})
}

func (a *App) ControlPlanePublicIP(actor domain.Actor) (string, error) {
	if err := actor.RequireUser(); err != nil {
		return "", err
	}
	return a.Config.PublicIP, nil
}

func pickPublicPort(protocol domain.EndpointProtocol, port int, requested *float64, own []domain.Endpoint, others []OwnedEndpoint) (int, error) {
	public, err := domain.PortNumber(requested)
	if err != nil {
		return 0, err
	}
	if public == nil {
		if i := slices.IndexFunc(own, func(e domain.Endpoint) bool { return e.Protocol == protocol && e.Port == port }); i >= 0 {
			return *own[i].PublicPort, nil
		}
		taken := map[int]bool{}
		for _, o := range others {
			if o.Protocol == protocol {
				taken[o.PublicPort] = true
			}
		}
		for _, e := range own {
			if e.Protocol == protocol {
				taken[*e.PublicPort] = true
			}
		}
		if protocol == domain.ProtocolTCP {
			taken[80], taken[443] = true, true
		}
		return domain.AllocatePublicPort(port, taken)
	}
	if protocol == domain.ProtocolTCP && domain.IsHTTPPort(*public) {
		return 0, domain.Invalid("80 and 443 serve HTTP; pick another public port")
	}
	for _, o := range others {
		if o.Protocol == protocol && o.PublicPort == *public {
			return 0, domain.Conflict("Port %d/%s is already used by %s", *public, protocol, o.Owner)
		}
	}
	return *public, nil
}

func followPort(tx Tx, ch *Changes, scope NodeScope, port int, now int64) (bool, error) {
	node, err := tx.Node(scope.Node.ID)
	if errors.Is(err, ErrNoRow) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	moved := false
	for i, e := range node.Endpoints {
		if !e.PinnedPort && e.Port != port {
			node.Endpoints[i].Port = port
			node.Endpoints[i].Status = domain.EndpointStatus{State: domain.EndpointStarting, At: now}
			moved = true
		}
	}
	if !moved {
		return false, nil
	}
	if err := tx.ReplaceEndpoints(node.ID, node.Endpoints); err != nil {
		return false, err
	}
	ch.Environment(scope.Org, node.EnvironmentID)
	return true, nil
}
