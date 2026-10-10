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

func (s StepStatus) Finished() bool { return s == StepDone || s == StepFailed }

type DeployStep struct {
	NodeID     string
	Label      string
	Status     StepStatus
	StartedAt  *int64
	AppliedAt  *int64
	FinishedAt *int64
}

type LogLine struct {
	At     int64
	NodeID string
	Text   string
}

type Deployment struct {
	ID            string
	EnvironmentID string
	Message       string
	Status        DeploymentStatus
	StartedAt     int64
	FinishedAt    *int64
	Steps         []DeployStep
	Log           []LogLine
}

type Variable struct {
	ID     string
	NodeID string
	Key    string
	Value  string
	Secret bool
}
