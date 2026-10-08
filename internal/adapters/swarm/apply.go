package swarm

import (
	"context"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

// Apply-side Docker calls (docs/go/spec/swarm-worker.md §4.1, D1–D6). Image references are passed
// as they are and services are created with QueryRegistry off, so tags stay unpinned (as the
// Convex worker did) and no registry auth is ever sent.

var _ app.Swarm = (*Swarm)(nil)

// ImageCached: GET /images/{image}/json; 404 → not cached.
func (s *Swarm) ImageCached(ctx context.Context, image string) (bool, error) {
	_, err := s.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// PullImage: POST /images/create, reading the progress stream to the end. Unlike dockerode's
// followProgress, an error reported inside the stream (errorDetail) fails the pull, so a bad
// image is an apply error instead of a service whose tasks get rejected.
func (s *Swarm) PullImage(ctx context.Context, image string) error {
	resp, err := s.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	return resp.Wait(ctx)
}

// ServiceVersion: GET /services/svc-<id>; 404 → found=false.
func (s *Swarm) ServiceVersion(ctx context.Context, nodeID string) (uint64, bool, error) {
	res, err := s.cli.ServiceInspect(ctx, serviceName(nodeID), client.ServiceInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return res.Service.Version.Index, true, nil
}

// CreateService: POST /services/create.
func (s *Swarm) CreateService(ctx context.Context, spec app.ServiceSpec) error {
	_, err := s.cli.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: toSpec(spec)})
	return err
}

// UpdateService: POST /services/svc-<id>/update?version=<version>, a full replace of the spec.
func (s *Swarm) UpdateService(ctx context.Context, version uint64, spec app.ServiceSpec) error {
	opts := client.ServiceUpdateOptions{Spec: toSpec(spec)}
	opts.Version.Index = version
	_, err := s.cli.ServiceUpdate(ctx, serviceName(spec.NodeID), opts)
	return err
}

// RemoveService: DELETE /services/svc-<id>; 404 ignored.
func (s *Swarm) RemoveService(ctx context.Context, nodeID string) error {
	return s.removeService(ctx, serviceName(nodeID))
}

// legacyTunnelLabel marks the cloudflared services of the Quick Tunnel era (removed 2026-10-06).
const legacyTunnelLabel = "keel.ingress"

// RemoveLegacyTunnels: GET /services?filters={"label":["keel.ingress"]}, then DELETE each (404
// ignored). A no-op once they are gone (proxy-ingress.md §5.8).
func (s *Swarm) RemoveLegacyTunnels(ctx context.Context) (int, error) {
	res, err := s.cli.ServiceList(ctx, client.ServiceListOptions{Filters: make(client.Filters).Add("label", legacyTunnelLabel)})
	if err != nil {
		return 0, err
	}
	for _, svc := range res.Items {
		if err := s.removeService(ctx, svc.ID); err != nil {
			return 0, err
		}
	}
	return len(res.Items), nil
}

func (s *Swarm) removeService(ctx context.Context, nameOrID string) error {
	_, err := s.cli.ServiceRemove(ctx, nameOrID, client.ServiceRemoveOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}
