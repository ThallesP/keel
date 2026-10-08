package domain

// NodeStatus is what the canvas shows for a node (convex/status.ts).
//
//	done: a one-shot image ran and every task exited 0.
//	stopping: scaled to 0, Swarm has not yet confirmed every task is gone.
type NodeStatus string

const (
	StatusHealthy   NodeStatus = "healthy"
	StatusDone      NodeStatus = "done"
	StatusDeploying NodeStatus = "deploying"
	StatusStopping  NodeStatus = "stopping"
	StatusError     NodeStatus = "error"
	StatusStopped   NodeStatus = "stopped"
	StatusPending   NodeStatus = "pending"
)

// Converged: observed matches desired and every replica runs, or every replica ran to completion.
func Converged(desired *Desired, observed *Observed) bool {
	if desired == nil || observed == nil {
		return false
	}
	if desired.Replicas == 0 {
		return observed.Running == 0
	}
	if observed.Revision != desired.Revision {
		return false
	}
	if observed.State == ObservedCompleted {
		completed := 0
		if observed.Completed != nil {
			completed = *observed.Completed
		}
		return completed >= desired.Replicas
	}
	return observed.State == ObservedOK && observed.Running >= desired.Replicas
}

// DeriveStatus mirrors convex/status.ts deriveStatus exactly.
func DeriveStatus(n Node) NodeStatus {
	d, o := n.Desired, n.Observed
	if d == nil || d.Revision == 0 {
		return StatusPending // never shipped
	}
	if n.ApplyError != "" {
		return StatusError // pull / spec failure; cleared on the next ship
	}
	if o == nil {
		return StatusDeploying // shipped, observe has not seen it yet
	}
	// At 0 replicas the service spec label still carries the revision, so observe catches up even
	// though Swarm drops the task history. Revision 0 means the service itself is gone.
	if d.Replicas == 0 {
		if o.Running > 0 {
			return StatusStopping
		}
		if o.Revision == 0 || o.Revision >= d.Revision {
			return StatusStopped
		}
		return StatusStopping
	}
	if o.Revision < d.Revision {
		return StatusDeploying
	}
	if o.State == ObservedCrashloop || o.State == ObservedFailed {
		return StatusError
	}
	if Converged(d, o) {
		if o.State == ObservedCompleted {
			return StatusDone
		}
		return StatusHealthy
	}
	return StatusDeploying
}
