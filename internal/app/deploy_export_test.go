package app

import "context"

func (a *App) TimeoutDeploymentForTest(ctx context.Context, id string) { a.timeoutDeployment(ctx, id) }
