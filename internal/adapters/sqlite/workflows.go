package sqlite

import "github.com/ThallesP/durable"

func (t *tx) StartRun(run durable.Run) error {
	return t.flows.CreateRunTx(t.ctx, t.sql, run)
}
