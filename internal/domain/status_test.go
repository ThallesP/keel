package domain

import "testing"

func TestDeriveStatus(t *testing.T) {
	cases := []struct {
		name string
		n    Node
		want NodeStatus
	}{
		{"never shipped", Node{}, StatusPending},
		{"revision 0", Node{Desired: &Desired{Revision: 0}}, StatusPending},
		{"apply error", Node{Desired: &Desired{Revision: 1, Replicas: 1}, ApplyError: "pull"}, StatusError},
		{"not observed", Node{Desired: &Desired{Revision: 1, Replicas: 1}}, StatusDeploying},
		{"healthy", Node{Desired: &Desired{Revision: 2, Replicas: 1}, Observed: &Observed{Revision: 2, Running: 1, State: ObservedOK}}, StatusHealthy},
		{"old revision", Node{Desired: &Desired{Revision: 2, Replicas: 1}, Observed: &Observed{Revision: 1, Running: 1, State: ObservedOK}}, StatusDeploying},
		{"crashloop", Node{Desired: &Desired{Revision: 2, Replicas: 1}, Observed: &Observed{Revision: 2, State: ObservedCrashloop}}, StatusError},
		{"one-shot done", Node{Desired: &Desired{Revision: 2, Replicas: 1}, Observed: &Observed{Revision: 2, State: ObservedCompleted, Completed: 1}}, StatusDone},
		{"scaled to 0, running", Node{Desired: &Desired{Revision: 3, Replicas: 0}, Observed: &Observed{Revision: 3, Running: 1}}, StatusStopping},
		{"scaled to 0, gone", Node{Desired: &Desired{Revision: 3, Replicas: 0}, Observed: &Observed{Revision: 0}}, StatusStopped},
		{"scaled to 0, behind", Node{Desired: &Desired{Revision: 3, Replicas: 0}, Observed: &Observed{Revision: 2}}, StatusStopping},
	}
	for _, c := range cases {
		if got := DeriveStatus(c.n); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDeployingStep(t *testing.T) {
	cases := []struct {
		o    *Observed
		want string
	}{
		{nil, "pulling image"},
		{&Observed{Revision: 1, State: ObservedOK}, "pulling image"},
		{&Observed{Revision: 2, State: ObservedUpdating}, "rolling out"},
		{&Observed{Revision: 2, State: ObservedOK}, "starting"},
	}
	for _, c := range cases {
		if got := DeployingStep(Node{Desired: &Desired{Revision: 2, Replicas: 1}, Observed: c.o}); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.o, got, c.want)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{"My API": "my-api", "  Ação! ": "acao", "---": "", "Acme_Support 2": "acme-support-2"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
