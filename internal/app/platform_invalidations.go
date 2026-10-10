package app

import (
	"context"
	"maps"
	"slices"
	"sync"
)

type Invalidations struct {
	mu    sync.Mutex
	byOrg map[string][]string
}

type invalidationsKey struct{}

func WithInvalidations(ctx context.Context) (context.Context, *Invalidations) {
	rec := &Invalidations{byOrg: map[string][]string{}}
	return context.WithValue(ctx, invalidationsKey{}, rec), rec
}

func InvalidationsFrom(ctx context.Context) *Invalidations {
	rec, _ := ctx.Value(invalidationsKey{}).(*Invalidations)
	return rec
}

func (r *Invalidations) Add(organizationID string, topics ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byOrg[organizationID] = append(r.byOrg[organizationID], topics...)
}

func (r *Invalidations) Topics(organizationID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var topics []string
	for org, list := range r.byOrg {
		if organizationID == "" || org == organizationID {
			topics = append(topics, list...)
		}
	}
	slices.Sort(topics)
	return slices.Compact(topics)
}

func recordInvalidations(ctx context.Context, ch *Changes) {
	rec := InvalidationsFrom(ctx)
	if rec == nil {
		return
	}
	for org, set := range ch.byOrg {
		rec.Add(org, slices.Collect(maps.Keys(set))...)
	}
}
