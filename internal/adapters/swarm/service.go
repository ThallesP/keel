package swarm

import (
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

const (
	overlayNetwork = "keel"
	labelService   = "keel.service"
	labelRevision  = "keel.revision"
)

func serviceName(nodeID string) string { return domain.ServicePrefix + nodeID }

func toSpec(s app.ServiceSpec) swarm.ServiceSpec {
	labels := func() map[string]string {
		return map[string]string{labelService: s.NodeID, labelRevision: strconv.Itoa(s.Revision)}
	}
	delay := 5 * time.Second
	attempts := uint64(5)
	replicas := uint64(max(s.Replicas, 0))
	failure := swarm.UpdateFailureActionRollback
	if s.OneShot {
		failure = swarm.UpdateFailureActionContinue
	}
	return swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: serviceName(s.NodeID), Labels: labels()},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:  s.Image,
				Env:    s.Env,
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
		UpdateConfig: &swarm.UpdateConfig{
			Parallelism:   1,
			Order:         swarm.UpdateOrderStartFirst,
			FailureAction: failure,
		},
	}
}

func engineArgs(image string, env []string) []string {
	if imageEngine(image) != "redis" {
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
