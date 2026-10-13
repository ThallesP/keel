package app

import "context"

func (a *App) PruneMetricsNow(ctx context.Context) { a.pruneMetrics(ctx) }

func (a *App) RollupMetricsNow(ctx context.Context) { a.rollupMetrics(ctx) }
