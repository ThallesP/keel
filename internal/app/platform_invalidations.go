package app

import (
	"context"
	"sort"
	"sync"
)

// Invalidations collects what one request's committed writes published, per organization
// (docs/go/spec/web-data.md §9.3, read-your-writes). The transport names them in the response's
// Keel-Invalidate header, so the dashboard refetches the affected queries before its mutation
// resolves, as a Convex mutation's promise resolved only once its subscribed queries reflected
// it. The WebSocket publication of the same topics still follows; refetching twice is harmless.
type Invalidations struct {
	mu    sync.Mutex
	byOrg map[string]map[string]struct{}
}

type invalidationsKey struct{}

// WithInvalidations is ctx carrying a fresh recorder: every a.write under ctx that commits adds
// what it publishes. Writes of jobs it schedules run under their own context and are not added.
func WithInvalidations(ctx context.Context) (context.Context, *Invalidations) {
	rec := &Invalidations{}
	return context.WithValue(ctx, invalidationsKey{}, rec), rec
}

// InvalidationsFrom is ctx's recorder, nil when there is none.
func InvalidationsFrom(ctx context.Context) *Invalidations {
	rec, _ := ctx.Value(invalidationsKey{}).(*Invalidations)
	return rec
}

// Add records topics published to organizationID. Safe for concurrent use; nil-safe.
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

// Topics is what was published to organizationID, deduped and sorted. organizationID "" means
// to any organization (a caller who had none when the request began: sign-up, founding).
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

// recordInvalidations adds what a committed write publishes to ctx's recorder, if any.
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
