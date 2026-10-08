//go:build unix

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup puts the command in a process group of its own, so a signal can reach what it starts
// too (`npm run dev` → node).
func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the command's whole process group.
func signalGroup(p *os.Process, sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return p.Signal(sig)
	}
	return syscall.Kill(-p.Pid, s)
}
