package app

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"

	"github.com/ThallesP/keel/internal/domain"
)

const (
	otelHeaders  = "OTEL_EXPORTER_OTLP_HEADERS"
	otelEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
)

func (a *App) otlpEndpoint() string { return cmp.Or(a.Config.OTLPURL, a.Config.SiteURL+"/otlp") }

type envVar struct {
	Key   string
	Value string
}

func tracingEnv(endpoint string, node domain.Node, env domain.Environment, key string, local bool) []envVar {
	envName := env.Name
	if local {
		envName = "local"
	}
	resource := "keel.service_id=" + url.PathEscape(node.ID) +
		",keel.environment_id=" + url.PathEscape(env.ID) +
		",deployment.environment.name=" + url.PathEscape(envName)
	var out []envVar
	if !local {
		out = append(out, envVar{otelEndpoint, endpoint})
	}
	return append(out,
		envVar{"OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"},
		envVar{otelHeaders, "Authorization=Bearer%20" + key},
		envVar{"OTEL_SERVICE_NAME", node.Name},
		envVar{"OTEL_RESOURCE_ATTRIBUTES", resource},
		envVar{"OTEL_TRACES_EXPORTER", "otlp"},
		envVar{"OTEL_METRICS_EXPORTER", "none"},
		envVar{"OTEL_LOGS_EXPORTER", "none"},
	)
}

func tracingOverridden(own []string, k string) bool {
	return slices.Contains(own, k) ||
		(k == otelHeaders && (slices.Contains(own, otelEndpoint) || slices.Contains(own, "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")))
}

func (a *App) withTracing(tx Tx, node domain.Node, env map[string]string) (map[string]string, error) {
	if node.Desired == nil || !node.Desired.Tracing {
		return env, nil
	}
	environment, err := tx.Environment(node.EnvironmentID)
	if err != nil {
		return nil, err
	}
	key, err := tx.OTLPKeyOf(node.EnvironmentID)
	if errors.Is(err, ErrNoRow) {
		return env, nil
	}
	if err != nil {
		return nil, err
	}
	own := slices.Collect(maps.Keys(env))
	out := maps.Clone(env)
	for _, v := range tracingEnv(a.otlpEndpoint(), node, environment, key, false) {
		if !tracingOverridden(own, v.Key) {
			out[v.Key] = v.Value
		}
	}
	return out, nil
}

func orgTracesState(tx Tx, org string) (domain.TracesState, error) {
	sink, ok, err := orgSinkOf(tx, org)
	if err != nil || !ok {
		return domain.TracesOff, err
	}
	if sink.Traces == "" {
		return domain.TracesOld, nil
	}
	return domain.TracesOn, nil
}

var tracesOffErr = map[domain.TracesState]error{domain.TracesOff: errNoSink, domain.TracesOld: errNoTraces}

func tracingScope(tx Tx, actor domain.Actor, nodeID string) (NodeScope, domain.TracesState, error) {
	scope, err := requireNode(tx, actor, nodeID)
	if err != nil {
		return NodeScope{}, "", err
	}
	if scope.Node.Type != domain.NodeService || scope.Node.Desired == nil {
		return NodeScope{}, "", domain.Invalid("Only services can be traced")
	}
	state, err := orgTracesState(tx, scope.Project.OrganizationID)
	return scope, state, err
}

func (a *App) SetNodeTracing(ctx context.Context, actor domain.Actor, nodeID string, on bool) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, state, err := tracingScope(tx, actor, nodeID)
		if err != nil {
			return err
		}
		if on {
			if err := tracesOffErr[state]; err != nil {
				return err
			}
			if _, err := a.ensureOTLPKey(tx, ch, scope.EnvScope); err != nil {
				return err
			}
		}
		node := scope.Node
		if node.Desired.Tracing == on {
			return nil
		}
		node.Desired.Tracing = on
		node.Dirty = true
		if err := tx.UpdateNode(node); err != nil {
			return err
		}
		ch.Environment(scope.Project.OrganizationID, node.EnvironmentID)
		ch.Node(scope.Project.OrganizationID, node.EnvironmentID, node.ID)
		return nil
	})
}

func (a *App) NodeTracing(ctx context.Context, actor domain.Actor, nodeID string) (*domain.TracingView, error) {
	var view *domain.TracingView
	err := a.read(ctx, func(tx Tx) error {
		scope, ok, err := ownedNode(tx, actor, nodeID)
		if err != nil || !ok {
			return err
		}
		node := scope.Node
		if node.Type != domain.NodeService || node.Desired == nil {
			return nil
		}
		key, err := tx.OTLPKeyOf(node.EnvironmentID)
		if err != nil && !errors.Is(err, ErrNoRow) {
			return err
		}
		vars, err := tx.CanvasVariables(node.ID)
		if err != nil {
			return err
		}
		own := make([]string, len(vars))
		for i, v := range vars {
			own[i] = v.Key
		}
		state, err := orgTracesState(tx, scope.Project.OrganizationID)
		if err != nil {
			return err
		}
		view = &domain.TracingView{Enabled: node.Desired.Tracing, Traces: state, Env: []domain.TracingEnvVar{}}
		for _, v := range tracingEnv(a.otlpEndpoint(), node, scope.Environment, domain.MaskOTLPKey(key), false) {
			view.Env = append(view.Env, domain.TracingEnvVar{
				Key: v.Key, Value: v.Value, Secret: v.Key == otelHeaders, Overridden: tracingOverridden(own, v.Key),
			})
		}
		return nil
	})
	return view, err
}

type LocalTracing struct {
	Env    map[string]string
	Reason string
}

func (a *App) LocalTracingEnv(ctx context.Context, actor domain.Actor, nodeID string) (LocalTracing, error) {
	var out LocalTracing
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, state, err := tracingScope(tx, actor, nodeID)
		if err != nil {
			return err
		}
		if err := tracesOffErr[state]; err != nil {
			out.Reason = err.Error()
			return nil
		}
		key, err := a.ensureOTLPKey(tx, ch, scope.EnvScope)
		if err != nil {
			return err
		}
		out.Env = map[string]string{}
		for _, v := range tracingEnv(a.otlpEndpoint(), scope.Node, scope.Environment, key, true) {
			out.Env[v.Key] = v.Value
		}
		return nil
	})
	return out, err
}

func (a *App) TracingPrompt(ctx context.Context, actor domain.Actor, nodeID, environmentID string) (string, error) {
	var service, project string
	err := a.read(ctx, func(tx Tx) error {
		if nodeID != "" {
			scope, ok, err := ownedNode(tx, actor, nodeID)
			if err != nil {
				return err
			}
			if ok && scope.Node.Type == domain.NodeService {
				service, project = scope.Node.Name, scope.Project.Slug
				return nil
			}
		}
		if environmentID != "" {
			scope, ok, err := ownedEnvironment(tx, actor, environmentID)
			if err != nil {
				return err
			}
			if ok {
				project = scope.Project.Slug
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return agentPrompt(service, project), nil
}
