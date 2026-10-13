//go:build linux

package agent

import (
	"os"
	"syscall"
)

func readHost(diskPath string) (Host, error) {
	stat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return Host{}, err
	}
	cpus, busy, total, err := parseProcStat(stat)
	if err != nil {
		return Host{}, err
	}
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return Host{}, err
	}
	memTotal, memAvailable, err := parseMeminfo(meminfo)
	if err != nil {
		return Host{}, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(diskPath, &fs); err != nil {
		return Host{}, err
	}
	block := uint64(fs.Bsize)
	return Host{
		CPUs: cpus, CPUBusy: busy, CPUTotal: total,
		MemoryTotalBytes: memTotal, MemoryAvailableBytes: memAvailable,
		DiskTotalBytes: fs.Blocks * block, DiskFreeBytes: fs.Bavail * block,
	}, nil
}
