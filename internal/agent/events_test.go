package agent

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type post struct {
	body   string
	resync bool
}

// fakePoster records PostEvents calls and answers from results in order (the last repeats).
type fakePoster struct {
	mu      sync.Mutex
	posts   []post
	results []bool
}

func (p *fakePoster) PostEvents(_ context.Context, body []byte, resync bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, post{string(body), resync})
	ok := true
	if len(p.results) > 0 {
		ok = p.results[0]
		if len(p.results) > 1 {
			p.results = p.results[1:]
		}
	}
	return ok
}

func (p *fakePoster) all() []post {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.posts)
}

func ev(typ, action, actor string, attrs map[string]string, timeNano int64) Event {
	return Event{Type: typ, Action: action, ActorID: actor, Attributes: attrs, TimeNano: timeNano,
		Raw: []byte(`{"Type":"` + typ + `","Action":"` + action + `"}`)}
}

func svcAttrs(service string) map[string]string {
	return map[string]string{labelServiceName: service, labelTaskName: service + ".1.t"}
}

func TestRelevant(t *testing.T) {
	tests := []struct {
		e    Event
		want bool
	}{
		{ev("container", "start", "c", svcAttrs("svc-a"), 0), true},
		{ev("container", "die", "c", svcAttrs("svc-a"), 0), true},
		{ev("container", "exec_start: sh", "c", svcAttrs("svc-a"), 0), false},
		{ev("container", "health_status: healthy", "c", svcAttrs("svc-a"), 0), false},
		{ev("container", "start", "c", svcAttrs("keel-agent"), 0), false},
		{ev("container", "start", "c", nil, 0), false},
		{ev("service", "update", "s", map[string]string{"name": "svc-a"}, 0), true},
		{ev("service", "update", "s", map[string]string{"name": "keel-agent"}, 0), false},
		{ev("node", "update", "n", nil, 0), true},
		{ev("network", "connect", "x", map[string]string{"name": "svc-a"}, 0), false},
	}
	for _, tt := range tests {
		if got := relevant(tt.e); got != tt.want {
			t.Errorf("relevant(%s %s %v) = %v, want %v", tt.e.Type, tt.e.Action, tt.e.Attributes, got, tt.want)
		}
	}
}

type containerHook struct{ action, id string }

func newTestForwarder(d *fakeDocker, p *fakePoster, state *State) (*Forwarder, *syncBuffer, *[]containerHook, *sync.Mutex) {
	buf := &syncBuffer{}
	var mu sync.Mutex
	var hooks []containerHook
	f := &Forwarder{
		Docker: d, Poster: p, State: state, Log: NewLogger(buf),
		OnContainer: func(action, id string, _ map[string]string) {
			mu.Lock()
			defer mu.Unlock()
			hooks = append(hooks, containerHook{action, id})
		},
		reconnect: time.Millisecond,
	}
	return f, buf, &hooks, &mu
}

func TestForwarderStream(t *testing.T) {
	d := newFakeDocker()
	d.streams = []eventScript{{
		events: []Event{
			ev("container", "start", "c1", svcAttrs("svc-a"), 1704067200000000001),
			ev("container", "exec_start: sh", "c1", svcAttrs("svc-a"), 1704067200000000002),
			ev("container", "health_status: healthy", "c1", svcAttrs("svc-a"), 1704067200000000003),
			ev("container", "start", "c9", svcAttrs("keel-agent"), 1704067200000000004),
			ev("service", "update", "s1", map[string]string{"name": "svc-b"}, 1704067200000000005),
			ev("node", "update", "n1", nil, 0),
			ev("network", "connect", "x", nil, 1704067200999999999),
		},
		end: io.EOF,
	}}
	p := &fakePoster{}
	state := LoadState(t.TempDir() + "/state.json")
	f, logs, hooks, mu := newTestForwarder(d, p, state)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool { return len(d.sinces()) >= 2 }) // reconnected after EOF
	cancel()
	<-done

	want := []post{
		{"[]", true},
		{`{"Type":"container","Action":"start"}`, false},
		{`{"Type":"service","Action":"update"}`, false},
		{`{"Type":"node","Action":"update"}`, false},
		{"[]", true}, // the reconnect asks for a sweep again
	}
	if got := p.all(); !slices.Equal(got, want) {
		t.Fatalf("posts = %+v\nwant %+v", got, want)
	}
	mu.Lock()
	wantHooks := []containerHook{{"start", "c1"}, {"exec_start: sh", "c1"}, {"health_status: healthy", "c1"}, {"start", "c9"}}
	if !slices.Equal(*hooks, wantHooks) {
		t.Errorf("container hooks = %v, want %v", *hooks, wantHooks)
	}
	mu.Unlock()
	if s := d.sinces(); s[0] != "" || s[1] != "1704067201.000000000" {
		t.Errorf("events since = %v", s)
	}
	if state.EventsSince() != "1704067201.000000000" {
		t.Errorf("state eventsSince = %q", state.EventsSince())
	}
	out := logs.String()
	for _, w := range []string{
		"[events] streaming docker events\n",
		"[events] docker events stream ended, reconnecting in 2s\n",
		"[events] streaming docker events since 1704067201.000000000\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

// A batch that was not accepted makes the next one ask for a resync, until one is accepted.
func TestForwarderResyncAfterFailure(t *testing.T) {
	d := newFakeDocker()
	d.streams = []eventScript{{
		events: []Event{
			ev("node", "update", "n", nil, 0),
			ev("node", "update", "n", nil, 0),
			ev("node", "update", "n", nil, 0),
			ev("node", "update", "n", nil, 0),
		},
		end: errors.New("unexpected EOF"),
	}}
	p := &fakePoster{results: []bool{false, true, false, true, true}}
	f, logs, _, _ := newTestForwarder(d, p, LoadState(t.TempDir()+"/s.json"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool { return len(p.all()) >= 5 })
	cancel()
	<-done
	got := p.all()[:5]
	want := []bool{true, true, false, true, false}
	for i, w := range want {
		if got[i].resync != w {
			t.Fatalf("resync flags = %+v, want %v", got, want)
		}
	}
	if !strings.Contains(logs.String(), "[events] stream error: unexpected EOF, reconnecting in 2s") {
		t.Errorf("log = %s", logs.String())
	}
}

func TestForwarderDockerDown(t *testing.T) {
	d := newFakeDocker()
	d.streams = []eventScript{{err: errors.New("connect: no such file or directory")}}
	p := &fakePoster{}
	f, logs, _, _ := newTestForwarder(d, p, LoadState(t.TempDir()+"/s.json"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool { return len(p.all()) >= 1 }) // second connect works
	cancel()
	<-done
	if !strings.Contains(logs.String(), "[events] stream error: connect: no such file or directory, reconnecting in 2s") {
		t.Fatalf("log = %s", logs.String())
	}
	if got := p.all(); got[0] != (post{"[]", true}) {
		t.Fatalf("a failed GET /events posted %+v first", got)
	}
}
