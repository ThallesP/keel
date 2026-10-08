package swarm

import (
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// The Swarm service of a canvas node (convex/swarm.ts toSpec; docs/go/spec/swarm-worker.md §5).

const (
	// overlayNetwork: every service attaches to the `keel` overlay (made by install, MTU 1200).
	overlayNetwork = "keel"
	labelService   = "keel.service"
	labelRevision  = "keel.revision"
)

// serviceName is svc-<nodeId>: the Swarm service and its overlay DNS name.
func serviceName(nodeID string) string { return domain.ServicePrefix + nodeID }

// toSpec is the full service spec of a node. Deliberately absent: EndpointSpec (nothing is
// published on the host; docs/networking.md, zero inbound ports), Placement, Mounts, Resources,
// HealthCheck, RollbackConfig, Monitor/Delay (Swarm defaults). An update replaces the whole spec,
// so drift made by hand is reverted on the next apply; the revision label changes on every ship,
// so every apply rolls new tasks.
func toSpec(s app.ServiceSpec) swarm.ServiceSpec {
	labels := func() map[string]string {
		return map[string]string{labelService: s.NodeID, labelRevision: strconv.Itoa(s.Revision)}
	}
	delay := 5 * time.Second // after MaxAttempts Swarm gives up and observe reports crashloop
	attempts := uint64(5)
	replicas := uint64(max(s.Replicas, 0))
	// One-shot images exit 0, which Swarm counts as a failed update inside the Monitor window:
	// `rollback` would undo every Redeploy of them, `continue` lets the new revision run once.
	failure := swarm.UpdateFailureActionRollback
	if s.OneShot {
		failure = swarm.UpdateFailureActionContinue
	}
	env := s.Env
	if env == nil {
		env = []string{}
	}
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: serviceName(s.NodeID), Labels: labels()},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:  s.Image,
				Env:    env,
				Args:   engineArgs(s.Image, s.Env),
				Labels: labels(),
			},
			RestartPolicy: &swarm.RestartPolicy{
				Condition:   swarm.RestartPolicyConditionOnFailure,
				Delay:       &delay,
				MaxAttempts: &attempts,
			},
			Networks: []swarm.NetworkAttachmentConfig{{Target: overlayNetwork}},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}},
		// start-first is only safe while services are stateless. See docs/volumes.md before
		// adding mounts.
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			Order:         swarm.UpdateOrderStartFirst,
			FailureAction: failure,
		},
	}
}

// engineArgs: Redis's image reads no password from its environment, so REDIS_PASSWORD (seeded
// at create) becomes `redis-server --requirepass <pass>`; the image's entrypoint runs it. nil for
// every other image, or without a non-empty password.
func engineArgs(image string, env []string) []string {
	if imageEngine(image) != "redis" {
		return nil
	}
	const key = "REDIS_PASSWORD="
	for _, e := range env {
		if strings.HasPrefix(e, key) {
			if pass := strings.TrimPrefix(e, key); pass != "" {
				return []string{"redis-server", "--requirepass", pass}
			}
			return nil
		}
	}
	return nil
}

// imageEngine is nodeHelpers.ts engineOf: the repository name of image (no registry, path, tag or
// digest) when it is one of the built-in engines, else "". `bitnami/redis:7` → redis.
func imageEngine(image string) string {
	ref, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	repo, _, _ := strings.Cut(ref, ":")
	switch repo {
	case "postgres", "mysql", "mongo", "redis":
		return repo
	}
	return ""
}
