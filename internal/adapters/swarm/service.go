package swarm

import (
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

const labelService = "keel.service"

func serviceName(nodeID string) string { return domain.ServicePrefix + nodeID }

func toSpec(s app.ServiceSpec) swarm.ServiceSpec {
	labels := map[string]string{labelService: s.NodeID, "keel.revision": strconv.Itoa(s.Revision)}
	failure := swarm.UpdateFailureActionRollback
	if s.OneShot {
		failure = swarm.UpdateFailureActionContinue
	}
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: serviceName(s.NodeID), Labels: labels},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:  s.Image,
				Env:    s.Env,
				Args:   engineArgs(s.Image, s.Env),
				Labels: labels,
			},
			RestartPolicy: &swarm.RestartPolicy{
				Condition:   swarm.RestartPolicyConditionOnFailure,
				Delay:       new(5 * time.Second),
				MaxAttempts: new(uint64(5)),
			},
			Networks: []swarm.NetworkAttachmentConfig{{Target: "keel"}},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: new(uint64(s.Replicas))}},
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			Order:         swarm.UpdateOrderStartFirst,
			FailureAction: failure,
		},
	}
}

func engineArgs(image string, env []string) []string {
	if domain.EngineOf(image) != domain.EngineRedis {
		return nil
	}
	for _, e := range env {
		if pass, ok := strings.CutPrefix(e, "REDIS_PASSWORD="); ok {
			if pass == "" {
				return nil
			}
			return []string{"redis-server", "--requirepass", pass}
		}
	}
	return nil
}
