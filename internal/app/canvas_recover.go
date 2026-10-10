package app

import (
	"context"
	"slices"

	"github.com/ThallesP/keel/internal/domain"
)

func (a *App) recoverCanvas(ctx context.Context) {
	n, err := a.canvasBackfillRedisPasswords(ctx)
	if err != nil {
		a.Log.Error("redis passwords", "err", err)
		return
	}
	if n > 0 {
		a.Log.Info("redis passwords added", "redisPasswords", n)
	}
}

func (a *App) canvasBackfillRedisPasswords(ctx context.Context) (int, error) {
	added := 0
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		nodes, err := tx.AllNodes()
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if n.Type != domain.NodeCache || n.Desired == nil || domain.EngineOf(n.Desired.Image) != domain.EngineRedis {
				continue
			}
			rows, err := tx.CanvasVariables(n.ID)
			if err != nil {
				return err
			}
			if slices.ContainsFunc(rows, func(r domain.Variable) bool { return r.Key == canvasRedisPassword }) {
				continue
			}
			v := domain.Variable{ID: domain.NewID(), NodeID: n.ID, Key: canvasRedisPassword, Value: domain.RandomSecret(20), Secret: true}
			if err := tx.CanvasInsertVariable(v); err != nil {
				return err
			}
			if err := tx.CanvasMarkDirty(n.ID); err != nil {
				return err
			}
			org, err := tx.OrganizationOfEnvironment(n.EnvironmentID)
			if err != nil {
				return err
			}
			if err := markReferrersDirty(tx, ch, org, n); err != nil {
				return err
			}
			if err := canvasTouch(tx, ch, org, n.EnvironmentID); err != nil {
				return err
			}
			added++
		}
		return nil
	})
	return added, err
}

const canvasRedisPassword = "REDIS_PASSWORD"
