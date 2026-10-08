package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

// The keel-agent global service: `keel agent` on every Swarm node, forwarding Docker events and
// shipping container logs to serve. Replaces scripts/deploy-worker.sh (docs/go/spec/swarm-worker.md
// §14.2) and keeps its settings: global mode, host network, the Docker socket mounted read-only,
// the keel-worker-state volume at /var/lib/keel-worker (the agent's state file survives the switch
// from the Bun worker), restart on any exit after 2 s, 10 s stop grace period, the image pinned by
// digest when Docker knows one and never resolved against a registry by Swarm.

const (
	agentServiceName = "keel-agent"
	agentSpecLabel   = "keel.agent.spec" // fingerprint of the spec below: unchanged → no update
	agentStateVolume = "keel-worker-state"
	agentStateDir    = "/var/lib/keel-worker"
	dockerSocketPath = "/var/run/docker.sock"
)

// legacyAgentServices are what keel-agent replaces: the Bun worker (deploy-worker.sh) and the
// shell forwarder before it. Running both would ship every log line twice.
var legacyAgentServices = []string{"keel-worker", "keel-events"}

// agentSpec is the service spec for image (already pinned).
func agentSpec(image string, a app.AgentSpec) swarm.ServiceSpec {
	restartDelay := 2 * time.Second
	grace := 10 * time.Second
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: agentServiceName, Labels: map[string]string{}},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:   image,
				Command: []string{"keel", "agent"},
				Env:     []string{"KEEL_URL=" + a.ControlURL, "KEEL_WORKER_TOKEN=" + a.Token},
				Mounts: []mount.Mount{
					{Type: mount.TypeBind, Source: dockerSocketPath, Target: dockerSocketPath, ReadOnly: true},
					{Type: mount.TypeVolume, Source: agentStateVolume, Target: agentStateDir},
				},
				StopGracePeriod: &grace,
			},
			RestartPolicy: &swarm.RestartPolicy{Condition: swarm.RestartPolicyConditionAny, Delay: &restartDelay},
			Networks:      []swarm.NetworkAttachmentConfig{{Target: "host"}},
		},
		Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}},
	}
	b, _ := json.Marshal(spec)
	sum := sha256.Sum256(b)
	spec.Labels[agentSpecLabel] = hex.EncodeToString(sum[:8])
	return spec
}

// EnsureAgent creates keel-agent, or updates it when its spec changed, then removes the legacy
// services it replaces.
func (s *Swarm) EnsureAgent(ctx context.Context, a app.AgentSpec) error {
	spec := agentSpec(s.pinnedImage(ctx, a.Image), a)
	res, err := s.cli.ServiceInspect(ctx, agentServiceName, client.ServiceInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		if _, err := s.cli.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: spec}); err != nil {
			return err
		}
	case err != nil:
		return err
	case res.Service.Spec.Labels[agentSpecLabel] != spec.Labels[agentSpecLabel]:
		opts := client.ServiceUpdateOptions{Spec: spec}
		opts.Version = res.Service.Version
		if _, err := s.cli.ServiceUpdate(ctx, agentServiceName, opts); err != nil {
			return err
		}
	}
	s.removeLegacyAgents(ctx)
	return nil
}

// pinnedImage is image@<digest> when the local image has a repo digest (pulled or pushed), so
// every node runs the same bytes and a moved tag still counts as a change; image as given
// otherwise (a local build on a single node). An image missing locally is pulled first.
func (s *Swarm) pinnedImage(ctx context.Context, image string) string {
	if strings.Contains(image, "@") {
		return image
	}
	inspect, err := s.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		if s.PullImage(ctx, image) != nil {
			return image
		}
		inspect, err = s.cli.ImageInspect(ctx, image)
	}
	if err != nil || len(inspect.RepoDigests) == 0 {
		return image
	}
	_, digest, ok := strings.Cut(inspect.RepoDigests[0], "@")
	if !ok || digest == "" {
		return image
	}
	return image + "@" + digest
}

// removeLegacyAgents removes the services keel-agent replaces and, best effort, their secrets and
// configs (a secret still held by a task that is shutting down goes on the next start).
func (s *Swarm) removeLegacyAgents(ctx context.Context) {
	for _, name := range legacyAgentServices {
		_ = s.removeService(ctx, name)
	}
	for _, prefix := range []string{"keel-worker-token-", "keel-events-token-"} {
		res, err := s.cli.SecretList(ctx, client.SecretListOptions{Filters: make(client.Filters).Add("name", prefix)})
		if err != nil {
			continue
		}
		for _, sec := range res.Items {
			if strings.HasPrefix(sec.Spec.Name, prefix) {
				_, _ = s.cli.SecretRemove(ctx, sec.ID, client.SecretRemoveOptions{})
			}
		}
	}
	res, err := s.cli.ConfigList(ctx, client.ConfigListOptions{Filters: make(client.Filters).Add("name", "keel-events-")})
	if err != nil {
		return
	}
	for _, c := range res.Items {
		if strings.HasPrefix(c.Spec.Name, "keel-events-") {
			_, _ = s.cli.ConfigRemove(ctx, c.ID, client.ConfigRemoveOptions{})
		}
	}
}
