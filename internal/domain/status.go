package domain

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
		return observed.Completed != nil && *observed.Completed >= desired.Replicas
	}
	return observed.State == ObservedOK && observed.Running >= desired.Replicas
}

func DeriveStatus(n Node) NodeStatus {
	d, o := n.Desired, n.Observed
	if d == nil || d.Revision == 0 {
		return StatusPending
	}
	if n.ApplyError != "" {
		return StatusError
	}
	if o == nil {
		return StatusDeploying
	}
	if d.Replicas == 0 {
		if o.Running == 0 && (o.Revision == 0 || o.Revision >= d.Revision) {
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

func DeployingStep(n Node) string {
	switch {
	case n.Observed == nil || n.Observed.Revision < n.Desired.Revision:
		return "pulling image"
	case n.Observed.State == ObservedUpdating:
		return "rolling out"
	default:
		return "starting"
	}
}
