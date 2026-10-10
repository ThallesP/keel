package agent

import (
	"cmp"
	"context"
	"errors"
	"io"
	"strings"
	"time"
)

type EventPoster interface {
	PostEvents(ctx context.Context, body []byte, resync bool) bool
}

type Forwarder struct {
	Docker      Docker
	Poster      EventPoster
	State       *State
	Log         *Logger
	OnContainer func(action, containerID string, attrs map[string]string)
	reconnect   time.Duration
}

func (f *Forwarder) Run(ctx context.Context) {
	reconnect := cmp.Or(f.reconnect, 2*time.Second)
	for {
		err := f.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		msg := "stream error: " + errorText(err)
		if errors.Is(err, io.EOF) {
			msg = "docker events stream ended"
		}
		f.Log.Log("events", msg+", reconnecting in 2s")
		if sleepCtx(ctx, reconnect) != nil {
			return
		}
	}
}

func (f *Forwarder) stream(ctx context.Context) error {
	since := f.State.EventsSince()
	s := f.Docker.Events(ctx, since)
	defer s.Close()
	f.Log.Log("events", "streaming docker events since "+cmp.Or(since, "now"))
	resync := !f.Poster.PostEvents(ctx, []byte("[]"), true)
	for {
		e, err := s.Next()
		if err != nil {
			return err
		}
		if e.Type == "container" && e.ActorID != "" {
			f.OnContainer(e.Action, e.ActorID, e.Attributes)
		}
		if relevant(e) {
			resync = !f.Poster.PostEvents(ctx, e.Raw, resync)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if e.TimeNano != 0 {
			f.State.SetEventsSince(dockerTime(time.Unix(0, e.TimeNano+1)))
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
