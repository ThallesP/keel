package app

import (
	"context"
	"time"
)

func (a *App) TimeoutDeploymentForTest(ctx context.Context, id string) error {
	return a.timeoutDeployment(ctx, id)
}

func (a *App) SetDeployTimeoutForTest(d time.Duration) { a.deploy.timeout = d }
