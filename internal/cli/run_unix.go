//go:build unix

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(p *os.Process, sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return p.Signal(sig)
	}
	return syscall.Kill(-p.Pid, s)
}
