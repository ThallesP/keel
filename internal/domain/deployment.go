package domain

type DeploymentStatus string

const (
	DeploymentRunning DeploymentStatus = "running"
	DeploymentSuccess DeploymentStatus = "success"
	DeploymentFailed  DeploymentStatus = "failed"
)

type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepRunning StepStatus = "running"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
)

type DeployStep struct {
	NodeID     string     `json:"nodeId,omitempty"`
	Label      string     `json:"label"`
	Status     StepStatus `json:"status"`
	StartedAt  *int64     `json:"startedAt,omitempty"`
	AppliedAt  *int64     `json:"appliedAt,omitempty"`
	FinishedAt *int64     `json:"finishedAt,omitempty"`
}

type LogLine struct {
	At     int64  `json:"at"`
	NodeID string `json:"nodeId,omitempty"`
	Text   string `json:"text"`
}

type Deployment struct {
	ID            string           `json:"id"`
	EnvironmentID string           `json:"environmentId"`
	Sha           string           `json:"sha,omitempty"`
	Message       string           `json:"message"`
	Status        DeploymentStatus `json:"status"`
	StartedAt     int64            `json:"startedAt"`
	FinishedAt    *int64           `json:"finishedAt,omitempty"`
	Steps         []DeployStep     `json:"steps"`
	Log           []LogLine        `json:"log"`
}

type Variable struct {
	ID     string `json:"id"`
	NodeID string `json:"nodeId"`
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}
