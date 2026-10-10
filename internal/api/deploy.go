package api

import "github.com/ThallesP/keel/internal/domain"

type ShipRequest struct {
	Only    []string `json:"only,omitempty" doc:"Node ids to deploy; absent = every node with staged changes"`
	Refresh bool     `json:"refresh,omitempty" doc:"Pull the images again (Redeploy, Retry)"`
}

type ShipResponse struct {
	ID string `json:"id"`
}

type DeployStep struct {
	NodeID     string `json:"nodeId,omitempty" doc:"Absent on the final health checks step"`
	Label      string `json:"label" doc:"Node name at ship time"`
	Status     string `json:"status" enum:"pending,running,done,failed"`
	StartedAt  *int64 `json:"startedAt,omitempty"`
	AppliedAt  *int64 `json:"appliedAt,omitempty" doc:"When Swarm took the new spec"`
	FinishedAt *int64 `json:"finishedAt,omitempty"`
}

type DeployLogLine struct {
	At     int64  `json:"at"`
	NodeID string `json:"nodeId,omitempty"`
	Text   string `json:"text"`
}

type Deployment struct {
	ID            string          `json:"id"`
	EnvironmentID string          `json:"environmentId"`
	Sha           string          `json:"sha,omitempty"`
	Message       string          `json:"message" example:"ship api, postgres"`
	Status        string          `json:"status" enum:"running,success,failed"`
	StartedAt     int64           `json:"startedAt"`
	FinishedAt    *int64          `json:"finishedAt,omitempty"`
	Steps         []DeployStep    `json:"steps" doc:"One per shipped node in canvas order, then health checks"`
	Log           []DeployLogLine `json:"log" doc:"The last 500 lines, oldest first"`
}

type DeploymentEnvelope struct {
	Deployment *Deployment `json:"deployment"`
}

func DeploymentOf(d domain.Deployment) Deployment {
	out := Deployment{
		ID:            d.ID,
		EnvironmentID: d.EnvironmentID,
		Sha:           d.Sha,
		Message:       d.Message,
		Status:        string(d.Status),
		StartedAt:     d.StartedAt,
		FinishedAt:    d.FinishedAt,
		Steps:         make([]DeployStep, 0, len(d.Steps)),
		Log:           make([]DeployLogLine, 0, len(d.Log)),
	}
	for _, s := range d.Steps {
		out.Steps = append(out.Steps, DeployStep{
			NodeID: s.NodeID, Label: s.Label, Status: string(s.Status),
			StartedAt: s.StartedAt, AppliedAt: s.AppliedAt, FinishedAt: s.FinishedAt,
		})
	}
	for _, l := range d.Log {
		out.Log = append(out.Log, DeployLogLine(l))
	}
	return out
}
