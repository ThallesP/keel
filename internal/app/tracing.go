package app

// Tracing for a service (convex/tracing.ts). A per-service switch, desired.tracing, staged like a
// variable: on the next ship, apply gives the container the OTEL_* variables below (withTracing),
// which point any standard OpenTelemetry SDK at the OTLP relay (otlp.go) with the environment's
// ingest key. `keel run` gets the same set for a local run. The app's own variables win.

import (
	"context"
	"errors"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

const (
	otelHeaders        = "OTEL_EXPORTER_OTLP_HEADERS"
	otelEndpoint       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	otelTracesEndpoint = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
)

// otlpEndpoint is where deployed services send spans: KEEL_OTLP_URL, else <site>/otlp.
func (a *App) otlpEndpoint() string {
	site := a.Config.OTLPURL
	if site == "" {
		site = a.Config.SiteURL + "/otlp"
	}
	return strings.TrimRight(site, "/")
}

// tracingEnv is the OTEL_* a traced process gets, in order. local: `keel run`, which has no
// endpoint here (the CLI adds the address its machine reaches the control plane at) and is
// tagged deployment.environment.name=local; otherwise it is the deployed service.
func tracingEnv(endpoint string, node domain.Node, env domain.Environment, key string, local bool) [][2]string {
	envName := env.Name
	if local {
		envName = "local"
	}
	resource := "keel.service_id=" + jsEncodeURIComponent(node.ID) +
		",keel.environment_id=" + jsEncodeURIComponent(env.ID) +
		",deployment.environment.name=" + jsEncodeURIComponent(envName)
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

// tracingOverridden: the service's own variables replace Keel's k, key by key, except that the
// ingest key only goes to Keel's endpoint: a service naming its own endpoint gets no headers.
func tracingOverridden(own map[string]bool, k string) bool {
	return own[k] || (k == otelHeaders && (own[otelEndpoint] || own[otelTracesEndpoint]))
}

// tracingVars is the variables withTracing adds to a node whose own env has the keys in own, in
// tracingEnv order; nil when tracing is off or the environment has no ingest key yet.
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
		return nil, nil // SetNodeTracing makes the key before the switch; without one, nothing to send
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

// withTracing is env (the node's computed variables) plus its tracing variables when its
// tracing switch is on; the node's own keys win. env is not modified. Turning tracing off and
// shipping removes them all (they are never stored). The Swarm spec should list the service's
// own variables first, then these in tracingEnv order (see TracingVarOrder).
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

// TracingVarOrder is the position of an OTEL_* variable withTracing adds (0–7), or -1 for any
// other key: the deploy area sorts the container env with it (own variables first).
func TracingVarOrder(key string) int {
	for i, k := range []string{otelEndpoint, "OTEL_EXPORTER_OTLP_PROTOCOL", otelHeaders, "OTEL_SERVICE_NAME",
		"OTEL_RESOURCE_ATTRIBUTES", "OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER"} {
		if k == key {
			return i
		}
	}
	return -1
}

// orgTracesState is whether the organization can store traces: no sink, a sink from before traces,
// or yes.
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

// tracingScope is tracing.scope: the caller's service and its organization's traces state.
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

// SetNodeTracing turns tracing on or off for a service. Staged: the container gets (or loses)
// the variables on the next ship. Turning it on needs somewhere to send spans and makes the
// environment's ingest key; turning it off has no precondition. An unchanged switch leaves
// dirty alone.
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
		ch.Environment(scope.Org, node.EnvironmentID) // the canvas counts dirty nodes
		ch.Node(scope.Org, node.EnvironmentID, node.ID)
		return nil
	})
}

// NodeTracing is a service's tracing as its Settings tab and `keel tracing` show it (the ingest
// key masked); nil when the node is not the caller's, not a service, or has no desired state.
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
		v := &domain.TracingView{Enabled: node.Desired.Tracing, Traces: state, Env: []domain.TracingEnvVar{}}
		for _, kv := range tracingEnv(a.otlpEndpoint(), node, scope.Environment, domain.MaskOTLPKey(key), false) {
			v.Env = append(v.Env, domain.TracingEnvVar{
				Key: kv[0], Value: kv[1], Secret: kv[0] == otelHeaders, Overridden: tracingOverridden(own, kv[0]),
			})
		}
		view = v
		return nil
	})
	return view, err
}

// LocalTracing is `keel run`'s tracing: the variables of a local run of the service, without
// the endpoint (the CLI adds its own). With nowhere to send spans, Env is nil and Reason says
// why (the run goes ahead without tracing).
type LocalTracing struct {
	Env    map[string]string
	Reason string
}

// LocalTracingEnv makes the environment's ingest key if needed.
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

// TracingPrompt is the agent prompt, naming the service (nodeID) or the project (environmentID)
// when the caller can see them. Never fails for missing or foreign ids.
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
