package app

import "github.com/ThallesP/durable"

type WorkflowsTx interface {
	StartRun(run durable.Run) error
}
