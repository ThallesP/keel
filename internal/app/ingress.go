package app

import (
	"context"
	"errors"
	"math"
	"strings"

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
			return domain.Invalid(domain.MsgOnlyExposable)
		}
		if engineIsRedis(node.Desired.Image) {
			has, err := tx.IngressHasVariable(node.ID, "REDIS_PASSWORD")
			if err != nil {
				return err
			}
			deployed := node.DeployedRevision != nil && *node.DeployedRevision == node.Desired.Revision
			if !has || node.Dirty || !deployed {
				return domain.Invalid(domain.MsgShipRedisFirst)
			}
		}
		protocol := in.Protocol
		if protocol == "" {
			protocol = domain.ProtocolTCP
			if node.Type == domain.NodeService {
				protocol = domain.ProtocolHTTP
			}
		}
		port := node.Desired.Port
		if in.Port != nil {
			p, err := exposePort(*in.Port)
			if err != nil {
				return err
			}
			port = &p
		} else if err := domain.ValidPort(port); err != nil {
			return err
		}
		if port == nil {
			return domain.Invalid(domain.MsgSetPortFirst)
		}
		ip := a.Config.PublicIP
		if ip == "" {
			return domain.E(domain.CodeUnavailable, domain.MsgNoPublicIP)
		}
		own := node.Endpoints
		others, err := tx.IngressOtherEndpoints(node.ID)
		if err != nil {
			return err
		}

		wanted := domain.Endpoint{Protocol: protocol, Port: *port}
		if protocol == domain.ProtocolHTTP {
			if in.PublicPort != nil {
				return domain.Invalid(domain.MsgHTTPPorts)
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
				return domain.Invalid(domain.MsgOnlyHTTPDomain)
			}
			if in.PublicPort == nil {
				for _, e := range own {
					if e.Protocol == protocol && e.Port == *port {
						out = e
						return nil
					}
				}
			}
			taken := map[int]bool{}
			for _, o := range others {
				if o.Protocol == protocol {
					taken[o.PublicPort] = true
				}
			}
			for _, e := range own {
				if e.Protocol == protocol && e.PublicPort != nil {
					taken[*e.PublicPort] = true
				}
			}
			if protocol == domain.ProtocolTCP {
				taken[80], taken[443] = true, true
			}
			var public int
			if in.PublicPort == nil {
				public, err = domain.AllocatePublicPort(*port, taken)
			} else {
				public, err = exposePort(*in.PublicPort)
			}
			if err != nil {
				return err
			}
			if protocol == domain.ProtocolTCP && domain.IsHTTPPort(public) {
				return domain.Invalid(domain.MsgTCPOnHTTPPort)
			}
			for _, o := range others {
				if o.Protocol == protocol && o.PublicPort == public {
					return domain.Conflict("Port %d/%s is already used by %s", public, protocol, o.Owner)
				}
			}
			wanted.PublicPort = &public
		}

		key := wanted.Key()
		for _, e := range own {
			if e.Key() == key && e.Port == wanted.Port {
				out = e
				return nil
			}
		}
		rest := make([]domain.Endpoint, 0, len(own)+1)
		for _, e := range own {
			if e.Key() != key {
				rest = append(rest, e)
			}
		}
		if len(rest) >= domain.MaxEndpoints {
			return domain.Invalid(domain.MsgTooManyEndpoints)
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
		if !all && !(in.Protocol != "" && named) {
			return domain.Invalid(domain.MsgNameTheEndpoint)
		}
		node := scope.Node
		if len(node.Endpoints) == 0 {
			return nil
		}
		var keep []domain.Endpoint
		if !all {
			name := ""
			if in.Domain != nil && *in.Domain != "" {
				if name, err = domain.ValidDomain(*in.Domain); err != nil {
					return err
				}
			}
			key, ok := unexposeKey(in.Protocol, name, in.PublicPort)
			for _, e := range node.Endpoints {
				if !ok || e.Key() != key {
					keep = append(keep, e)
				}
			}
		}
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

func followPort(tx Tx, ch *Changes, scope NodeScope, port int, now int64) (bool, error) {
	node, err := tx.Node(scope.Node.ID)
	if errors.Is(err, ErrNoRow) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	eps := append([]domain.Endpoint(nil), node.Endpoints...)
	moved := false
	for i, e := range eps {
		if !e.PinnedPort && e.Port != port {
			eps[i].Port = port
			eps[i].Status = domain.EndpointStatus{State: domain.EndpointStarting, At: now}
			moved = true
		}
	}
	if !moved {
		return false, nil
	}
	if err := tx.ReplaceEndpoints(node.ID, eps); err != nil {
		return false, err
	}
	ch.Environment(scope.Org, node.EnvironmentID)
	return true, nil
}

func exposePort(f float64) (int, error) {
	if f != math.Trunc(f) || f < 1 || f > 65535 {
		return 0, domain.Invalid(domain.MsgPortRange)
	}
	return int(f), nil
}

func unexposeKey(protocol domain.EndpointProtocol, name string, publicPort *float64) (string, bool) {
	if protocol == domain.ProtocolHTTP {
		return ingressKey(protocol, name, 0), true
	}
	if publicPort == nil {
		return "", false
	}
	p, err := exposePort(*publicPort)
	if err != nil {
		return "", false
	}
	return ingressKey(protocol, "", p), true
}

func ingressKey(protocol domain.EndpointProtocol, name string, publicPort int) string {
	e := domain.Endpoint{Protocol: protocol, Domain: name}
	if protocol != domain.ProtocolHTTP {
		e.PublicPort = &publicPort
	}
	return e.Key()
}

func engineIsRedis(image string) bool {
	repo, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	repo, _, _ = strings.Cut(repo, ":")
	return repo == "redis"
}
