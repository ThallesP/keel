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

type fakePoster struct {
	mu      sync.Mutex
	posts   []post
	results []bool
}

func (p *fakePoster) PostEvents(_ context.Context, body []byte, resync bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, post{string(body), resync})
	return next(&p.results, true)
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
	return map[string]string{labelServiceName: service}
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

func newTestForwarder(d *fakeDocker, p EventPoster, state *State) (*Forwarder, *syncBuffer, func() []containerHook) {
	logs := &syncBuffer{}
	var mu sync.Mutex
	var hooks []containerHook
	f := &Forwarder{
		Docker: d, Poster: p, State: state, Log: NewLogger(logs),
		OnContainer: func(action, id string, _ map[string]string) {
			mu.Lock()
			defer mu.Unlock()
			hooks = append(hooks, containerHook{action, id})
		},
		reconnect: time.Millisecond,
	}
	return f, logs, func() []containerHook {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(hooks)
	}
}

func runUntil(t *testing.T, f *Forwarder, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { f.Run(ctx) })
	waitFor(t, cond)
	cancel()
	wg.Wait()
}

func TestForwarderStream(t *testing.T) {
	d := newFakeDocker()
	d.streams = []fakeEvents{{
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
	f, logs, hooks := newTestForwarder(d, p, state)
	runUntil(t, f, func() bool { return len(d.sinces()) >= 2 })

	want := []post{
		{"[]", true},
		{`{"Type":"container","Action":"start"}`, false},
		{`{"Type":"service","Action":"update"}`, false},
		{`{"Type":"node","Action":"update"}`, false},
		{"[]", true},
	}
	if got := p.all(); !slices.Equal(got, want) {
		t.Fatalf("posts = %+v\nwant %+v", got, want)
	}
	wantHooks := []containerHook{{"start", "c1"}, {"exec_start: sh", "c1"}, {"health_status: healthy", "c1"}, {"start", "c9"}}
	if got := hooks(); !slices.Equal(got, wantHooks) {
		t.Errorf("container hooks = %v, want %v", got, wantHooks)
	}
	if s := d.sinces(); s[0] != "" || s[1] != "1704067201.000000000" {
		t.Errorf("events since = %v", s)
	}
	if state.EventsSince() != "1704067201.000000000" {
		t.Errorf("state eventsSince = %q", state.EventsSince())
	}
	out := logs.String()
	for _, w := range []string{
		"[events] streaming docker events since now\n",
		"[events] docker events stream ended, reconnecting in 2s\n",
		"[events] streaming docker events since 1704067201.000000000\n",
	} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
}

func TestForwarderResyncAfterFailure(t *testing.T) {
	d := newFakeDocker()
	d.streams = []fakeEvents{{
		events: []Event{
			ev("node", "update", "n", nil, 0),
			ev("node", "update", "n", nil, 0),
			ev("node", "update", "n", nil, 0),
			ev("node", "update", "n", nil, 0),
		},
		end: errors.New("unexpected EOF"),
	}}
	p := &fakePoster{results: []bool{false, true, false, true, true}}
	f, logs, _ := newTestForwarder(d, p, LoadState(t.TempDir()+"/s.json"))
	runUntil(t, f, func() bool { return len(p.all()) >= 5 })
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

type cancellingPoster struct {
	cancel context.CancelFunc
	n      int
}

func (p *cancellingPoster) PostEvents(ctx context.Context, _ []byte, _ bool) bool {
	p.n++
	if p.n < 3 {
		return true
	}
	p.cancel()
	<-ctx.Done()
	return false
}

func TestForwarderShutdownMidPostKeepsTheEvent(t *testing.T) {
	d := newFakeDocker()
	d.streams = []fakeEvents{{events: []Event{
		ev("node", "update", "n", nil, 1704067200000000001),
		ev("node", "update", "n", nil, 1704067200000000005),
	}}}
	state := LoadState(t.TempDir() + "/s.json")
	ctx, cancel := context.WithCancel(context.Background())
	f, _, _ := newTestForwarder(d, &cancellingPoster{cancel: cancel}, state)
	f.Run(ctx)
	if got := state.EventsSince(); got != "1704067200.000000002" {
		t.Fatalf("eventsSince = %q, want just after the delivered event", got)
	}
}
