package app

import (
	"context"
	"sort"
	"sync"
)

type Invalidations struct {
	mu    sync.Mutex
	byOrg map[string]map[string]struct{}
}

type invalidationsKey struct{}

func WithInvalidations(ctx context.Context) (context.Context, *Invalidations) {
	rec := &Invalidations{}
	return context.WithValue(ctx, invalidationsKey{}, rec), rec
}

func InvalidationsFrom(ctx context.Context) *Invalidations {
	rec, _ := ctx.Value(invalidationsKey{}).(*Invalidations)
	return rec
}

func (r *Invalidations) Add(organizationID string, topics ...string) {
	if r == nil || organizationID == "" || len(topics) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byOrg == nil {
		r.byOrg = map[string]map[string]struct{}{}
	}
	set := r.byOrg[organizationID]
	if set == nil {
		set = map[string]struct{}{}
		r.byOrg[organizationID] = set
	}
	for _, t := range topics {
		if t != "" {
			set[t] = struct{}{}
		}
	}
}

func (r *Invalidations) Topics(organizationID string) []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]struct{}{}
	for org, set := range r.byOrg {
		if organizationID != "" && org != organizationID {
			continue
		}
		for t := range set {
			seen[t] = struct{}{}
		}
	}
	topics := make([]string, 0, len(seen))
	for t := range seen {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	return topics
}

func recordInvalidations(ctx context.Context, ch *Changes) {
	rec := InvalidationsFrom(ctx)
	if rec == nil {
		return
	}
	for org, set := range ch.byOrg {
		for t := range set {
			rec.Add(org, t)
		}
	}
}
