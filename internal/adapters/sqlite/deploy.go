package sqlite

import (
	"errors"

	"github.com/ThallesP/keel/internal/adapters/sqlite/db"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Implements app.DeployTx. Owner: the deploy area.

// deployLogCap: a deployment keeps its last 500 log lines (convex/deployments.ts MAX_LOG).
const deployLogCap = 500

func deploymentOf(r db.Deployment) domain.Deployment {
	return domain.Deployment{
		ID:            r.ID,
		EnvironmentID: r.EnvironmentID,
		Sha:           str(r.Sha),
		Message:       r.Message,
		Status:        domain.DeploymentStatus(r.Status),
		StartedAt:     r.StartedAt,
		FinishedAt:    r.FinishedAt,
		Steps:         []domain.DeployStep{},
	}
}

func deployStepOf(r db.DeploymentStep) domain.DeployStep {
	return domain.DeployStep{
		NodeID:     str(r.NodeID),
		Label:      r.Label,
		Status:     domain.StepStatus(r.Status),
		StartedAt:  r.StartedAt,
		AppliedAt:  r.AppliedAt,
		FinishedAt: r.FinishedAt,
	}
}

func (t *tx) deploySteps(id string) ([]domain.DeployStep, error) {
	rows, err := t.q.DeployListSteps(t.ctx, id)
	if err != nil {
		return nil, err
	}
	steps := make([]domain.DeployStep, 0, len(rows))
	for _, r := range rows {
		steps = append(steps, deployStepOf(r))
	}
	return steps, nil
}

// deployFill attaches steps and, when withLog, the log.
func (t *tx) deployFill(r db.Deployment, withLog bool) (domain.Deployment, error) {
	d := deploymentOf(r)
	steps, err := t.deploySteps(d.ID)
	if err != nil {
		return domain.Deployment{}, err
	}
	d.Steps = steps
	if withLog {
		if d.Log, err = t.DeploymentLog(d.ID); err != nil {
			return domain.Deployment{}, err
		}
	}
	return d, nil
}

func (t *tx) deployFillAll(rows []db.Deployment, withLog bool) ([]domain.Deployment, error) {
	out := make([]domain.Deployment, 0, len(rows))
	for _, r := range rows {
		d, err := t.deployFill(r, withLog)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (t *tx) HasRunningDeployment(environmentID string) (bool, error) {
	n, err := t.q.DeployHasRunning(t.ctx, environmentID)
	return n > 0, err
}

func (t *tx) insertSteps(id string, steps []domain.DeployStep) error {
	for i, s := range steps {
		err := t.q.DeployInsertStep(t.ctx, db.DeployInsertStepParams{
			DeploymentID: id,
			Idx:          int64(i),
			NodeID:       nullStr(s.NodeID),
			Label:        s.Label,
			Status:       string(s.Status),
			StartedAt:    s.StartedAt,
			AppliedAt:    s.AppliedAt,
			FinishedAt:   s.FinishedAt,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) InsertDeployment(d domain.Deployment) error {
	err := t.q.DeployInsert(t.ctx, db.DeployInsertParams{
		ID:            d.ID,
		EnvironmentID: d.EnvironmentID,
		Sha:           nullStr(d.Sha),
		Message:       d.Message,
		Status:        string(d.Status),
		StartedAt:     d.StartedAt,
		FinishedAt:    d.FinishedAt,
	})
	if err != nil {
		return err
	}
	return t.insertSteps(d.ID, d.Steps)
}

func (t *tx) Deployment(id string) (domain.Deployment, error) {
	r, err := t.q.DeployGet(t.ctx, id)
	if err != nil {
		return domain.Deployment{}, noRow(err)
	}
	return t.deployFill(r, true)
}

func (t *tx) LatestDeployment(environmentID string) (domain.Deployment, error) {
	r, err := t.q.DeployLatest(t.ctx, environmentID)
	if err != nil {
		return domain.Deployment{}, noRow(err)
	}
	return t.deployFill(r, true)
}

func (t *tx) RecentDeployments(environmentID string, limit int) ([]domain.Deployment, error) {
	rows, err := t.q.DeployListRecent(t.ctx, db.DeployListRecentParams{EnvironmentID: environmentID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return t.deployFillAll(rows, false)
}

func (t *tx) DeploymentLog(id string) ([]domain.LogLine, error) {
	rows, err := t.q.DeployListLog(t.ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]domain.LogLine, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.LogLine{At: r.At, NodeID: str(r.NodeID), Text: r.Text})
	}
	return out, nil
}

func (t *tx) RunningDeployments(environmentID string) ([]domain.Deployment, error) {
	var (
		rows []db.Deployment
		err  error
	)
	if environmentID == "" {
		rows, err = t.q.DeployListRunning(t.ctx)
	} else {
		rows, err = t.q.DeployListRunningInEnvironment(t.ctx, environmentID)
	}
	if err != nil {
		return nil, err
	}
	return t.deployFillAll(rows, true)
}

func (t *tx) UpdateDeployment(d domain.Deployment, appended []domain.LogLine) error {
	n, err := t.q.DeployUpdate(t.ctx, db.DeployUpdateParams{ID: d.ID, Status: string(d.Status), FinishedAt: d.FinishedAt})
	if err != nil {
		return err
	}
	if n == 0 {
		return app.ErrNoRow
	}
	if err := t.q.DeployDeleteSteps(t.ctx, d.ID); err != nil {
		return err
	}
	if err := t.insertSteps(d.ID, d.Steps); err != nil {
		return err
	}
	if len(appended) == 0 {
		return nil
	}
	for _, l := range appended {
		err := t.q.DeployInsertLog(t.ctx, db.DeployInsertLogParams{DeploymentID: d.ID, At: l.At, NodeID: nullStr(l.NodeID), Text: l.Text})
		if err != nil {
			return err
		}
	}
	return t.q.DeployTrimLog(t.ctx, db.DeployTrimLogParams{DeploymentID: d.ID, Offset: deployLogCap})
}

func (t *tx) ClusterServers() (int, error) {
	n, err := t.q.DeployGetCluster(t.ctx)
	if errors.Is(noRow(err), app.ErrNoRow) {
		return 0, nil
	}
	return int(n), err
}

func (t *tx) SetClusterServers(servers int, at int64) error {
	return t.q.DeploySetCluster(t.ctx, db.DeploySetClusterParams{Servers: int64(servers), At: at})
}

func (t *tx) DeployOrganizationIDs() ([]string, error) {
	return t.q.DeployListOrganizationIDs(t.ctx)
}
