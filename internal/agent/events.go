package agent

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/api/types/events"

	"github.com/ThallesP/keel/internal/domain"
)

type EventPoster interface {
	PostEvents(ctx context.Context, evs []events.Message, resync bool) bool
}

type Forwarder struct {
	Docker      Docker
	Poster      EventPoster
	State       *State
	Log         *slog.Logger
	OnContainer func(action, containerID string, attrs map[string]string)
	reconnect   time.Duration
}

func (f *Forwarder) Run(ctx context.Context) {
	for {
		err := f.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		f.Log.Error("docker events stream ended, reconnecting", "err", err)
		if sleepCtx(ctx, f.reconnect) != nil {
			return
		}
	}
}

func (f *Forwarder) stream(ctx context.Context) error {
	s := f.Docker.Events(ctx, f.State.EventsSince())
	resync := !f.Poster.PostEvents(ctx, []events.Message{}, true)
	for {
		e, err := s.Next()
		if err != nil {
			return err
		}
		if e.Type == "container" {
			f.OnContainer(string(e.Action), e.Actor.ID, e.Actor.Attributes)
		}
		if relevant(e) {
			resync = !f.Poster.PostEvents(ctx, []events.Message{e}, resync)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		f.State.SetEventsSince(dockerTime(time.Unix(0, e.TimeNano+1)))
	}
}

func relevant(e events.Message) bool {
	if strings.HasPrefix(string(e.Action), "exec_") || strings.HasPrefix(string(e.Action), "health_status") {
		return false
	}
	switch e.Type {
	case "container":
		return strings.HasPrefix(e.Actor.Attributes[labelServiceName], domain.ServicePrefix)
	case "service":
		return strings.HasPrefix(e.Actor.Attributes["name"], domain.ServicePrefix)
	case "node":
		return true
	default:
		return false
	}
}
