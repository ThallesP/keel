//go:build !unix

package cli

import (
	"os"
	"os/exec"
)

func ownGroup(*exec.Cmd) {}

func signalGroup(p *os.Process, sig os.Signal) error { return p.Signal(sig) }
