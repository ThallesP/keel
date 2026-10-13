//go:build !linux

package agent

import "errors"

func readHost(string) (Host, error) {
	return Host{}, errors.New("host metrics are read from /proc, Linux only")
}
