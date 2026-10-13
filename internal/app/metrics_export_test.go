package app

import "context"

func (a *App) PruneMetricsNow(ctx context.Context) { a.pruneMetrics(ctx) }
