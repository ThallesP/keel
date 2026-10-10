package app

import (
	"maps"
	"slices"
)

type Changes struct {
	byOrg map[string]map[string]struct{}
	after []func()
}

func (c *Changes) AfterCommit(fn func()) { c.after = append(c.after, fn) }

func (c *Changes) Add(organizationID string, topics ...string) {
	if organizationID == "" {
		return
	}
	if c.byOrg == nil {
		c.byOrg = map[string]map[string]struct{}{}
	}
	set := c.byOrg[organizationID]
	if set == nil {
		set = map[string]struct{}{}
		c.byOrg[organizationID] = set
	}
	for _, t := range topics {
		set[t] = struct{}{}
	}
}

func (c *Changes) Projects(org string) { c.Add(org, "/api/projects") }

func (c *Changes) Environment(org, environmentID string) {
	c.Add(org, "/api/environments/"+environmentID, "/api/nodes/")
}

func (c *Changes) Node(org, environmentID, nodeID string) {
	c.Add(org, "/api/nodes/"+nodeID, "/api/environments/"+environmentID)
}

func (c *Changes) Deployment(org, environmentID, deploymentID string) {
	c.Add(org, "/api/deployments/"+deploymentID, "/api/environments/"+environmentID)
}

func (c *Changes) Organization(org string) { c.Add(org, "/api/organization") }

func (c *Changes) publish(p Publisher) {
	for org, set := range c.byOrg {
		p.Publish(org, slices.Sorted(maps.Keys(set)))
	}
	for _, fn := range c.after {
		fn()
	}
}
