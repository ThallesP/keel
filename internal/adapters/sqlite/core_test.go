package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func OpenTest(t testing.TB) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedEnvironment(t *testing.T, s *Store) string {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Default', 'default', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Acme', 'acme', 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1)`,
	} {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return "env"
}

func TestNodeRoundTrip(t *testing.T) {
	s := OpenTest(t)
	env := seedEnvironment(t, s)
	want := domain.Node{
		ID: domain.NewID(), EnvironmentID: env, Type: domain.NodeService, Name: "api",
		Position: domain.Position{X: 1.5, Y: -2},
		Desired:  &domain.Desired{Image: "nginx:1", Revision: 3, Replicas: 2, Port: 8080, Tracing: true},
		Observed: &domain.Observed{Revision: 3, Running: 2, Completed: 1, FinishedAt: 99,
			State: domain.ObservedOK, At: 42},
		Dirty: true, ApplyError: "boom", OneShot: true, CreatedAt: 7,
	}
	eps := []domain.Endpoint{
		{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api.example.com", Status: domain.EndpointStatus{State: domain.EndpointLive, At: 1}},
		{Protocol: domain.ProtocolTCP, Port: 5432, PublicPort: new(5432), PinnedPort: true, Status: domain.EndpointStatus{State: domain.EndpointFailed, Error: "in use", At: 2}},
	}
	ctx := context.Background()
	err := s.Write(ctx, func(tx app.Tx) error {
		if err := tx.InsertNode(want); err != nil {
			return err
		}
		return tx.ReplaceEndpoints(want.ID, eps)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got domain.Node
	if err := s.Read(ctx, func(tx app.Tx) (err error) { got, err = tx.Node(want.ID); return }); err != nil {
		t.Fatal(err)
	}
	for i := range got.Endpoints {
		if got.Endpoints[i].ID == "" || got.Endpoints[i].NodeID != want.ID {
			t.Fatalf("endpoint %d: %+v", i, got.Endpoints[i])
		}
		eps[i].ID, eps[i].NodeID = got.Endpoints[i].ID, want.ID
	}
	want.Endpoints = eps
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}

	got.Desired, got.Observed, got.Name = nil, nil, "api-2"
	if err := s.Write(ctx, func(tx app.Tx) error { return tx.UpdateNode(got) }); err != nil {
		t.Fatal(err)
	}
	err = s.Read(ctx, func(tx app.Tx) error {
		n, err := tx.Node(want.ID)
		if err != nil {
			return err
		}
		if n.Desired != nil || n.Observed != nil || n.Name != "api-2" {
			t.Errorf("after update: %+v", n)
		}
		if _, err := tx.Node("missing"); !errors.Is(err, app.ErrNoRow) {
			t.Errorf("missing node: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
