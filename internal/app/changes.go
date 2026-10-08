package app

import "sort"

// Changes collects what a write touched, as URL path prefixes of the API (docs/go/ARCHITECTURE.md,
// "Realtime"). The dashboard refetches every query whose path starts with one of them.
type Changes struct {
	byOrg map[string]map[string]struct{}
}

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

// Projects: the project list.
func (c *Changes) Projects(org string) { c.Add(org, "/api/projects") }

// Project: one project (and the list, which shows it).
func (c *Changes) Project(org, projectID string) {
	c.Add(org, "/api/projects")
}

// Environment: the canvas, summary, deployments and anything else under /api/environments/<id>.
func (c *Changes) Environment(org, environmentID string) {
	c.Add(org, "/api/environments/"+environmentID)
}

// Node: the node's own endpoints (/api/nodes/<id>/...) and its environment's canvas.
func (c *Changes) Node(org, environmentID, nodeID string) {
	c.Add(org, "/api/nodes/"+nodeID, "/api/environments/"+environmentID)
}

// Deployment: one deployment and its environment's lists.
func (c *Changes) Deployment(org, environmentID, deploymentID string) {
	c.Add(org, "/api/deployments/"+deploymentID, "/api/environments/"+environmentID)
}

// Organization: members, invitations, the log sink, the current organization.
func (c *Changes) Organization(org string) { c.Add(org, "/api/organization") }

// Everything: every query of the organization.
func (c *Changes) Everything(org string) { c.Add(org, "/api") }

func (c *Changes) publish(p Publisher) {
	for org, set := range c.byOrg {
		topics := make([]string, 0, len(set))
		for t := range set {
			topics = append(topics, t)
		}
		sort.Strings(topics)
		p.Publish(org, topics)
	}
}
