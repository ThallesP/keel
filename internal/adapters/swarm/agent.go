package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

const (
	agentServiceName  = "keel-agent"
	agentSpecLabel    = "keel.agent.spec"
	agentSecretPrefix = "keel-agent-token-"
	dockerSocketPath  = "/var/run/docker.sock"
)

func agentSecretName(token string) string {
	sum := sha256.Sum256([]byte(token))
	return agentSecretPrefix + hex.EncodeToString(sum[:6])
}

func agentSpec(image string, a app.AgentSpec, secretID string) swarm.ServiceSpec {
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: agentServiceName, Labels: map[string]string{}},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{
				Image:   image,
				Command: []string{"keel", "agent"},
				Env:     []string{"KEEL_URL=" + a.ControlURL},
				Secrets: []*swarm.SecretReference{{
					SecretID:   secretID,
					SecretName: agentSecretName(a.Token),
					File:       &swarm.SecretReferenceFileTarget{Name: "keel_worker_token", UID: "0", GID: "0", Mode: 0o400},
				}},
				Mounts: []mount.Mount{
					{Type: mount.TypeBind, Source: dockerSocketPath, Target: dockerSocketPath, ReadOnly: true},
					{Type: mount.TypeVolume, Source: "keel-agent-state", Target: "/var/lib/keel-agent"},
				},
				StopGracePeriod: new(10 * time.Second),
				CapabilityDrop:  []string{"ALL"},
				Privileges:      &swarm.Privileges{NoNewPrivileges: true},
			},
			RestartPolicy: &swarm.RestartPolicy{Condition: swarm.RestartPolicyConditionAny, Delay: new(2 * time.Second)},
			Networks:      []swarm.NetworkAttachmentConfig{{Target: "host"}},
		},
		Mode: swarm.ServiceMode{Global: &swarm.GlobalService{}},
	}
	b, _ := json.Marshal(spec)
	sum := sha256.Sum256(b)
	spec.Labels[agentSpecLabel] = hex.EncodeToString(sum[:8])
	return spec
}

func (s *Swarm) EnsureAgent(ctx context.Context, a app.AgentSpec) error {
	secretID, err := s.ensureAgentSecret(ctx, a.Token)
	if err != nil {
		return err
	}
	spec := agentSpec(s.pinnedImage(ctx, a.Image), a, secretID)
	res, err := s.cli.ServiceInspect(ctx, agentServiceName, client.ServiceInspectOptions{})
	switch {
	case cerrdefs.IsNotFound(err):
		_, err = s.cli.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: spec})
	case err == nil && res.Service.Spec.Labels[agentSpecLabel] != spec.Labels[agentSpecLabel]:
		_, err = s.cli.ServiceUpdate(ctx, agentServiceName, client.ServiceUpdateOptions{Spec: spec, Version: res.Service.Version})
	}
	if err != nil {
		return err
	}
	s.removeStaleAgentSecrets(ctx, agentSecretName(a.Token))
	return nil
}

func (s *Swarm) ensureAgentSecret(ctx context.Context, token string) (string, error) {
	name := agentSecretName(token)
	res, err := s.cli.SecretList(ctx, client.SecretListOptions{Filters: make(client.Filters).Add("name", name)})
	if err != nil {
		return "", err
	}
	if i := slices.IndexFunc(res.Items, func(sec swarm.Secret) bool { return sec.Spec.Name == name }); i >= 0 {
		return res.Items[i].ID, nil
	}
	created, err := s.cli.SecretCreate(ctx, client.SecretCreateOptions{Spec: swarm.SecretSpec{
		Annotations: swarm.Annotations{Name: name, Labels: map[string]string{"keel.agent": "token"}},
		Data:        []byte(token),
	}})
	return created.ID, err
}

func (s *Swarm) removeStaleAgentSecrets(ctx context.Context, keep string) {
	res, err := s.cli.SecretList(ctx, client.SecretListOptions{Filters: make(client.Filters).Add("name", agentSecretPrefix)})
	if err != nil {
		return
	}
	for _, sec := range res.Items {
		if sec.Spec.Name != keep {
			_, _ = s.cli.SecretRemove(ctx, sec.ID, client.SecretRemoveOptions{})
		}
	}
}

func (s *Swarm) pinnedImage(ctx context.Context, image string) string {
	if strings.Contains(image, "@") {
		return image
	}
	inspect, err := s.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) && s.PullImage(ctx, image) == nil {
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
