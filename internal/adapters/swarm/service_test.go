package swarm

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/ThallesP/keel/internal/app"
)

func compact(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestToSpecGolden pins the exact JSON Docker receives (docs/go/spec/swarm-worker.md §5).
// ForceUpdate and MaxFailureRatio are the moby types' non-omitempty zero values (= absent).
func TestToSpecGolden(t *testing.T) {
	cases := []struct {
		name string
		in   app.ServiceSpec
		want string
	}{
		{"service", app.ServiceSpec{NodeID: "k57abc", Image: "nginx:alpine", Revision: 3, Replicas: 2, Env: []string{"A=1", "PORT=80"}}, `{
			"Name": "svc-k57abc",
			"Labels": {"keel.revision": "3", "keel.service": "k57abc"},
			"TaskTemplate": {
				"ContainerSpec": {
					"Image": "nginx:alpine",
					"Labels": {"keel.revision": "3", "keel.service": "k57abc"},
					"Env": ["A=1", "PORT=80"]
				},
				"RestartPolicy": {"Condition": "on-failure", "Delay": 5000000000, "MaxAttempts": 5},
				"Networks": [{"Target": "keel"}],
				"ForceUpdate": 0
			},
			"Mode": {"Replicated": {"Replicas": 2}},
			"UpdateConfig": {"Parallelism": 1, "FailureAction": "rollback", "MaxFailureRatio": 0, "Order": "start-first"}
		}`},
		{"redis gets its password as an argument; one-shot continues", app.ServiceSpec{NodeID: "r1", Image: "bitnami/redis:7", Revision: 1, Replicas: 0, Env: []string{"REDIS_PASSWORD=s3cret", "REDIS_PASSWORD=second"}, OneShot: true}, `{
			"Name": "svc-r1",
			"Labels": {"keel.revision": "1", "keel.service": "r1"},
			"TaskTemplate": {
				"ContainerSpec": {
					"Image": "bitnami/redis:7",
					"Labels": {"keel.revision": "1", "keel.service": "r1"},
					"Args": ["redis-server", "--requirepass", "s3cret"],
					"Env": ["REDIS_PASSWORD=s3cret", "REDIS_PASSWORD=second"]
				},
				"RestartPolicy": {"Condition": "on-failure", "Delay": 5000000000, "MaxAttempts": 5},
				"Networks": [{"Target": "keel"}],
				"ForceUpdate": 0
			},
			"Mode": {"Replicated": {"Replicas": 0}},
			"UpdateConfig": {"Parallelism": 1, "FailureAction": "continue", "MaxFailureRatio": 0, "Order": "start-first"}
		}`},
		{"no env", app.ServiceSpec{NodeID: "e", Image: "redis:7", Revision: 1, Replicas: 1}, `{
			"Name": "svc-e",
			"Labels": {"keel.revision": "1", "keel.service": "e"},
			"TaskTemplate": {
				"ContainerSpec": {"Image": "redis:7", "Labels": {"keel.revision": "1", "keel.service": "e"}},
				"RestartPolicy": {"Condition": "on-failure", "Delay": 5000000000, "MaxAttempts": 5},
				"Networks": [{"Target": "keel"}],
				"ForceUpdate": 0
			},
			"Mode": {"Replicated": {"Replicas": 1}},
			"UpdateConfig": {"Parallelism": 1, "FailureAction": "rollback", "MaxFailureRatio": 0, "Order": "start-first"}
		}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(toSpec(c.in))
		if err != nil {
			t.Fatal(err)
		}
		if want := compact(t, c.want); string(got) != want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
	}
}

func TestEngineArgs(t *testing.T) {
	cases := []struct {
		image string
		env   []string
		want  []string
	}{
		{"redis:7", []string{"REDIS_PASSWORD=p"}, []string{"redis-server", "--requirepass", "p"}},
		{"docker.io/library/redis:7.2@sha256:abc", []string{"X=1", "REDIS_PASSWORD=p"}, []string{"redis-server", "--requirepass", "p"}},
		{"redis:7", []string{"REDIS_PASSWORD="}, nil},
		{"redis:7", nil, nil},
		{"postgres:16", []string{"REDIS_PASSWORD=p"}, nil},
		{"my-redis:1", []string{"REDIS_PASSWORD=p"}, nil},
	}
	for _, c := range cases {
		got := engineArgs(c.image, c.env)
		if len(got) != len(c.want) || (len(got) > 0 && got[2] != c.want[2]) {
			t.Errorf("engineArgs(%q, %v) = %v", c.image, c.env, got)
		}
	}
}

func TestImageEngine(t *testing.T) {
	for in, want := range map[string]string{
		"postgres:16": "postgres", "docker.io/library/mysql:8.4": "mysql", "mongo": "mongo",
		"bitnami/redis:7": "redis", "redis@sha256:abc": "redis", "nginx": "", "ghcr.io/acme/postgres-tools:1": "",
		"localhost:5000/redis": "redis",
	} {
		if got := imageEngine(in); got != want {
			t.Errorf("imageEngine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentSpec(t *testing.T) {
	spec := agentSpec("ghcr.io/thallesp/keel:1.0@sha256:abc", app.AgentSpec{ControlURL: "http://100.64.0.1:8080", Token: "tok"})
	got, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := compact(t, `{
		"Name": "keel-agent",
		"Labels": {"keel.agent.spec": "`+spec.Labels[agentSpecLabel]+`"},
		"TaskTemplate": {
			"ContainerSpec": {
				"Image": "ghcr.io/thallesp/keel:1.0@sha256:abc",
				"Command": ["keel", "agent"],
				"Env": ["KEEL_URL=http://100.64.0.1:8080", "KEEL_WORKER_TOKEN=tok"],
				"Mounts": [
					{"Type": "bind", "Source": "/var/run/docker.sock", "Target": "/var/run/docker.sock", "ReadOnly": true},
					{"Type": "volume", "Source": "keel-worker-state", "Target": "/var/lib/keel-worker"}
				],
				"StopGracePeriod": 10000000000
			},
			"RestartPolicy": {"Condition": "any", "Delay": 2000000000},
			"Networks": [{"Target": "host"}],
			"ForceUpdate": 0
		},
		"Mode": {"Global": {}}
	}`)
	if string(got) != want {
		t.Errorf("agent spec:\n got %s\nwant %s", got, want)
	}
	// The fingerprint moves with anything that matters and only then.
	same := agentSpec("ghcr.io/thallesp/keel:1.0@sha256:abc", app.AgentSpec{ControlURL: "http://100.64.0.1:8080", Token: "tok"})
	other := agentSpec("ghcr.io/thallesp/keel:1.0@sha256:abc", app.AgentSpec{ControlURL: "http://100.64.0.1:8080", Token: "rotated"})
	if same.Labels[agentSpecLabel] != spec.Labels[agentSpecLabel] || other.Labels[agentSpecLabel] == spec.Labels[agentSpecLabel] {
		t.Error("fingerprint")
	}
}
