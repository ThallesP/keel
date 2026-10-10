package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

const (
	otelHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	otelEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	otelTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
)

func (a *App) otlpEndpoint() string {
	endpoint := a.Config.OTLPURL
	if endpoint == "" {
		endpoint = a.Config.SiteURL + "/otlp"
	}
	return strings.TrimRight(endpoint, "/")
}

func tracingEnv(endpoint string, node domain.Node, env domain.Environment, key string, local bool) [][2]string {
	envName := env.Name
	if local {
		envName = "local"
	}
	resource := "keel.service_id=" + domain.EncodeURIComponent(node.ID) +
		",keel.environment_id=" + domain.EncodeURIComponent(env.ID) +
		",deployment.environment.name=" + domain.EncodeURIComponent(envName)
	var out [][2]string
	if !local {
		out = append(out, [2]string{otelEndpoint, endpoint})
	}
	return append(out,
		[2]string{"OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"},
		[2]string{otelHeaders, "Authorization=Bearer%20" + key},
		[2]string{"OTEL_SERVICE_NAME", node.Name},
		[2]string{"OTEL_RESOURCE_ATTRIBUTES", resource},
		[2]string{"OTEL_TRACES_EXPORTER", "otlp"},
		[2]string{"OTEL_METRICS_EXPORTER", "none"},
		[2]string{"OTEL_LOGS_EXPORTER", "none"},
	)
}

func tracingOverridden(own map[string]bool, k string) bool {
	return own[k] || (k == otelHeaders && (own[otelEndpoint] || own[otelTracesEndpoint]))
}

func (a *App) tracingVars(tx Tx, node domain.Node, own map[string]bool) ([][2]string, error) {
	if node.Desired == nil || !node.Desired.Tracing {
		return nil, nil
	}
	env, err := tx.Environment(node.EnvironmentID)
	if errors.Is(err, ErrNoRow) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := tx.OTLPKeyOf(node.EnvironmentID)
	if errors.Is(err, ErrNoRow) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, kv := range tracingEnv(a.otlpEndpoint(), node, env, key, false) {
		if !tracingOverridden(own, kv[0]) {
			out = append(out, kv)
		}
	}
	return out, nil
}

func (a *App) withTracing(tx Tx, node domain.Node, env map[string]string) (map[string]string, error) {
	own := make(map[string]bool, len(env))
	for k := range env {
		own[k] = true
	}
	added, err := a.tracingVars(tx, node, own)
	if err != nil || len(added) == 0 {
		return env, err
	}
	out := make(map[string]string, len(env)+len(added))
	for k, v := range env {
		out[k] = v
	}
	for _, kv := range added {
		out[kv[0]] = kv[1]
	}
	return out, nil
}

func TracingVarOrder(key string) int {
	return slices.Index([]string{otelEndpoint, "OTEL_EXPORTER_OTLP_PROTOCOL", otelHeaders, "OTEL_SERVICE_NAME",
		"OTEL_RESOURCE_ATTRIBUTES", "OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER"}, key)
}

func orgTracesState(tx Tx, org string) (string, error) {
	rec, err := orgSinkOf(tx, org)
	if err != nil {
		return "", err
	}
	if rec == nil || rec.Sink.Kind != domain.SinkKindAxiom {
		return domain.TracesOff, nil
	}
	if rec.Sink.Traces == "" {
		return domain.TracesOld, nil
	}
	return domain.TracesOn, nil
}

func requireTracesOn(state string) error {
	switch state {
	case domain.TracesOff:
		return errTracesOff(msgNoSink)
	case domain.TracesOld:
		return errTracesOff(msgNoTraces)
	}
	return nil
}

func tracingScope(tx Tx, actor domain.Actor, nodeID string) (NodeScope, string, error) {
	scope, err := requireNode(tx, actor, nodeID)
	if err != nil {
		return NodeScope{}, "", err
	}
	if scope.Node.Type != domain.NodeService || scope.Node.Desired == nil {
		return NodeScope{}, "", domain.Invalid(msgOnlyServicesTrace)
	}
	state, err := orgTracesState(tx, scope.Org)
	return scope, state, err
}

func (a *App) SetNodeTracing(ctx context.Context, actor domain.Actor, nodeID string, on bool) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		scope, state, err := tracingScope(tx, actor, nodeID)
		if err != nil {
			return err
		}
		if on {
			if err := requireTracesOn(state); err != nil {
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
		d := *node.Desired
		d.Tracing = on
		node.Desired = &d
		node.Dirty = true
		if err := tx.UpdateNode(node); err != nil {
			return err
		}
		ch.Environment(scope.Org, node.EnvironmentID)
		ch.Node(scope.Org, node.EnvironmentID, node.ID)
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
		keys, err := tx.TracingVariableKeys(node.ID)
		if err != nil {
			return err
		}
		own := map[string]bool{}
		for _, k := range keys {
			own[k] = true
		}
		state, err := orgTracesState(tx, scope.Org)
		if err != nil {
			return err
		}
		view = &domain.TracingView{Enabled: node.Desired.Tracing, Traces: state, Env: []domain.TracingEnvVar{}}
		for _, kv := range tracingEnv(a.otlpEndpoint(), node, scope.Environment, domain.MaskOTLPKey(key), false) {
			view.Env = append(view.Env, domain.TracingEnvVar{
				Key: kv[0], Value: kv[1], Secret: kv[0] == otelHeaders, Overridden: tracingOverridden(own, kv[0]),
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
		if state != domain.TracesOn {
			out.Reason = msgNoTraces
			if state == domain.TracesOff {
				out.Reason = msgNoSink
			}
			return nil
		}
		key, err := a.ensureOTLPKey(tx, ch, scope.EnvScope)
		if err != nil {
			return err
		}
		out.Env = map[string]string{}
		for _, kv := range tracingEnv(a.otlpEndpoint(), scope.Node, scope.Environment, key, true) {
			out.Env[kv[0]] = kv[1]
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
