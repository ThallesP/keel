package swarm

import (
	"context"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/ThallesP/keel/internal/app"
)

var _ app.Swarm = (*Swarm)(nil)

func (s *Swarm) ImageCached(ctx context.Context, image string) (bool, error) {
	_, err := s.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (s *Swarm) PullImage(ctx context.Context, image string) error {
	resp, err := s.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	return resp.Wait(ctx)
}

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

func (s *Swarm) CreateService(ctx context.Context, spec app.ServiceSpec) error {
	_, err := s.cli.ServiceCreate(ctx, client.ServiceCreateOptions{Spec: toSpec(spec)})
	return err
}

func (s *Swarm) UpdateService(ctx context.Context, version uint64, spec app.ServiceSpec) error {
	opts := client.ServiceUpdateOptions{Spec: toSpec(spec), Version: swarm.Version{Index: version}}
	_, err := s.cli.ServiceUpdate(ctx, serviceName(spec.NodeID), opts)
	return err
}

func (s *Swarm) RemoveService(ctx context.Context, nodeID string) error {
	_, err := s.cli.ServiceRemove(ctx, serviceName(nodeID), client.ServiceRemoveOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}
