package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

type Forwarder struct {
	Docker Docker
	Poster interface {
		PostEvents(ctx context.Context, body []byte, resync bool) bool
	}
	State       *State
	Log         *Logger
	OnContainer func(action, containerID string, attrs map[string]string)

	reconnect time.Duration
	sleep     func(context.Context, time.Duration) error
}

func (f *Forwarder) Run(ctx context.Context) {
	reconnect := f.reconnect
	if reconnect == 0 {
		reconnect = 2 * time.Second
	}
	sleep := f.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	for ctx.Err() == nil {
		f.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		if sleep(ctx, reconnect) != nil {
			return
		}
	}
}

func (f *Forwarder) stream(ctx context.Context) {
	since := f.State.EventsSince()
	s, err := f.Docker.Events(ctx, since)
	if err != nil {
		if ctx.Err() == nil {
			f.Log.Log("events", "stream error: "+err.Error()+", reconnecting in 2s")
		}
		return
	}
	defer s.Close()
	if since != "" {
		f.Log.Log("events", "streaming docker events since "+since)
	} else {
		f.Log.Log("events", "streaming docker events")
	}
	resync := !f.Poster.PostEvents(ctx, []byte("[]"), true)
	for {
		e, err := s.Next()
		if err != nil {
			switch {
			case ctx.Err() != nil:
			case errors.Is(err, io.EOF):
				f.Log.Log("events", "docker events stream ended, reconnecting in 2s")
			default:
				f.Log.Log("events", "stream error: "+err.Error()+", reconnecting in 2s")
			}
			return
		}
		if e.Type == "container" && e.ActorID != "" {
			f.OnContainer(e.Action, e.ActorID, e.Attributes)
		}
		if relevant(e) {
			resync = !f.Poster.PostEvents(ctx, e.Raw, resync)
			if ctx.Err() != nil {
				return
			}
		}
		if e.TimeNano != 0 {
			f.State.SetEventsSince(eventsSinceAfter(e.TimeNano))
		}
	}
}

func relevant(e Event) bool {
	if strings.HasPrefix(e.Action, "exec_") || strings.HasPrefix(e.Action, "health_status") {
		return false
	}
	switch e.Type {
	case "container":
		return strings.HasPrefix(e.Attributes[labelServiceName], "svc-")
	case "service":
		return strings.HasPrefix(e.Attributes["name"], "svc-")
	case "node":
		return true
	default:
		return false
	}
}
