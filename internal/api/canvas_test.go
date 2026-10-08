package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

func TestNodeViewOf(t *testing.T) {
	i := func(v int) *int { return &v }
	ms := func(v int64) *int64 { return &v }
	shipped := ms(5000)
	desired := func(rev, replicas int) *domain.Desired {
		return &domain.Desired{Image: "nginx:alpine", Revision: rev, Replicas: replicas, Port: i(80)}
	}
	obs := func(rev, running int, state domain.ObservedState) *domain.Observed {
		return &domain.Observed{Revision: rev, Running: running, State: state, NodeIDs: []string{}, At: 1}
	}
	cases := []struct {
		name string
		n    domain.Node
		want func(v *NodeView)
	}{
		{"pending", domain.Node{Type: domain.NodeService, Desired: desired(0, 1), Dirty: true}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Dirty = "pending", "nginx:alpine", i(80), 1, true
		}},
		{"deploying, not observed yet", domain.Node{Type: domain.NodeService, Desired: desired(1, 1), ShippedAt: shipped}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision = "deploying", "nginx:alpine", i(80), 1, 1
			v.Deploy = &NodeDeploy{Step: "pulling image", StartedAt: 5000}
		}},
		{"deploying, old revision", domain.Node{Type: domain.NodeService, Desired: desired(2, 1), ShippedAt: shipped, Observed: obs(1, 1, domain.ObservedOK)}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.Running = "deploying", "nginx:alpine", i(80), 1, 2, 1
			v.Deploy = &NodeDeploy{Step: "pulling image", StartedAt: 5000}
		}},
		{"rolling out", domain.Node{Type: domain.NodeService, Desired: desired(2, 2), ShippedAt: shipped, Observed: obs(2, 1, domain.ObservedUpdating)}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.Running = "deploying", "nginx:alpine", i(80), 2, 2, 1
			v.Deploy = &NodeDeploy{Step: "rolling out", StartedAt: 5000}
		}},
		{"starting", domain.Node{Type: domain.NodeService, Desired: desired(2, 2), ShippedAt: shipped, Observed: obs(2, 1, domain.ObservedOK)}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.Running = "deploying", "nginx:alpine", i(80), 2, 2, 1
			v.Deploy = &NodeDeploy{Step: "starting", StartedAt: 5000}
		}},
		{"deploying without shippedAt", domain.Node{Type: domain.NodeService, Desired: desired(1, 1)}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision = "deploying", "nginx:alpine", i(80), 1, 1
		}},
		{"apply error", domain.Node{Type: domain.NodeService, Desired: desired(1, 1), ApplyError: "pull failed", Observed: &domain.Observed{Revision: 1, Error: "task err", State: domain.ObservedOK}}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.Error = "error", "nginx:alpine", i(80), 1, 1, "pull failed"
		}},
		{"crash loop", domain.Node{Type: domain.NodeService, Desired: desired(1, 1), Observed: &domain.Observed{Revision: 1, Error: "exit 1", State: domain.ObservedCrashloop}}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.Error = "error", "nginx:alpine", i(80), 1, 1, "exit 1"
		}},
		{"healthy keeps an old task error out", domain.Node{Type: domain.NodeService, Desired: desired(1, 1), DeployedRevision: i(1), Observed: &domain.Observed{Revision: 1, Running: 1, Error: "old", State: domain.ObservedOK}}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.Running, v.DeployedRevision = "healthy", "nginx:alpine", i(80), 1, 1, 1, i(1)
		}},
		{"stopped", domain.Node{Type: domain.NodeService, Desired: desired(3, 0), ShippedAt: shipped, Observed: obs(0, 0, domain.ObservedOK)}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Revision, v.StoppedAt = "stopped", "nginx:alpine", i(80), 3, shipped
		}},
		{"stopping", domain.Node{Type: domain.NodeService, Desired: desired(3, 0), ShippedAt: shipped, Observed: obs(3, 1, domain.ObservedOK)}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Revision, v.Running, v.StoppedAt = "stopping", "nginx:alpine", i(80), 3, 1, shipped
		}},
		{"done", domain.Node{Type: domain.NodeService, Desired: desired(1, 1), ShippedAt: shipped,
			Observed: &domain.Observed{Revision: 1, State: domain.ObservedCompleted, Completed: i(1), FinishedAt: ms(9000)}}, func(v *NodeView) {
			v.Status, v.Image, v.Port, v.Replicas, v.Revision, v.FinishedAt = "done", "nginx:alpine", i(80), 1, 1, ms(9000)
		}},
		{"group in nothing", domain.Node{Type: domain.NodeGroup, ParentID: "", Config: domain.DefaultConfig(domain.NodeGroup)}, func(v *NodeView) {
			v.Type, v.Status, v.Config = "group", "pending", NodeConfig{Width: canvasFloat(300), Height: canvasFloat(180)}
		}},
		{"volume in a group", domain.Node{Type: domain.NodeVolume, ParentID: "g1", Position: domain.Position{X: 1.5, Y: 2}, Config: domain.DefaultConfig(domain.NodeVolume)}, func(v *NodeView) {
			v.Type, v.Status, v.ParentID, v.Position, v.Config = "volume", "pending", "g1", Position{X: 1.5, Y: 2}, NodeConfig{SizeGb: canvasFloat(10)}
		}},
	}
	for _, c := range cases {
		c.n.ID, c.n.Name = "n1", "api"
		if c.n.Type == "" {
			c.n.Type = domain.NodeService
		}
		want := NodeView{ID: "n1", Name: "api", Type: "service", Endpoints: []EndpointView{}}
		c.want(&want)
		if got := NodeViewOf(c.n, "203.0.113.7"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, want)
		}
	}
}

func canvasFloat(v float64) *float64 { return &v }

func TestNodeViewEndpoints(t *testing.T) {
	pub := 5432
	n := domain.Node{ID: "n1", Name: "pg", Type: domain.NodeDatabase, Desired: &domain.Desired{Image: "postgres:16", Revision: 1, Replicas: 1}, Endpoints: []domain.Endpoint{
		{Protocol: domain.ProtocolTCP, Port: 5432, PublicPort: &pub, Status: domain.EndpointStatus{State: domain.EndpointLive}},
		{Protocol: domain.ProtocolHTTP, Port: 80, Domain: "a.example.com", Status: domain.EndpointStatus{State: domain.EndpointFailed, Error: "no cert"}},
		{Protocol: domain.ProtocolHTTP, Port: 80, Domain: "b.example.com", Status: domain.EndpointStatus{State: domain.EndpointLive}},
	}}
	v := NodeViewOf(n, "")
	if !v.Public || v.PublicURL != "https://a.example.com" || len(v.Endpoints) != 3 {
		t.Fatalf("view %+v", v)
	}
	if e := v.Endpoints[0]; e.Address != "<public IP>:5432" || e.State != "live" || e.PublicPort == nil || *e.PublicPort != 5432 {
		t.Errorf("tcp endpoint %+v", e)
	}
	if e := v.Endpoints[1]; e.Address != "https://a.example.com" || e.Error != "no cert" || e.State != "failed" {
		t.Errorf("http endpoint %+v", e)
	}
}

func TestNodeViewJSON(t *testing.T) {
	b, err := json.Marshal(NodeViewOf(domain.Node{ID: "g", Name: "group", Type: domain.NodeGroup, Config: domain.DefaultConfig(domain.NodeGroup)}, ""))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"g","type":"group","name":"group","position":{"x":0,"y":0},"config":{"width":300,"height":180},"dirty":false,"status":"pending","replicas":0,"running":0,"revision":0,"public":false,"endpoints":[]}`
	if string(b) != want {
		t.Errorf("json\n got %s\nwant %s", b, want)
	}
	// Variable parts carry exactly one of text / ref.
	text := VariablePartOf(domain.RefPart{Text: "a"})
	ref := VariablePartOf(domain.RefPart{Ref: &domain.Ref{Key: "K", Missing: true}})
	b, _ = json.Marshal([]VariablePart{text, ref})
	if string(b) != `[{"text":"a"},{"ref":{"key":"K","missing":true}}]` {
		t.Errorf("parts json %s", b)
	}
}
