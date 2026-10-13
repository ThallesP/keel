package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThallesP/keel/internal/api"
)

type Host struct {
	CPUs                 int
	CPUBusy              uint64
	CPUTotal             uint64
	MemoryTotalBytes     uint64
	MemoryAvailableBytes uint64
	DiskTotalBytes       uint64
	DiskFreeBytes        uint64
}

type Sampler struct {
	Docker Docker
	Host   func() (Host, error)
	Post   func(context.Context, api.MetricsReport) error
	Log    *slog.Logger
	Every  time.Duration

	server   api.MetricsServer
	last     map[string]ContainerStats
	lastHost *Host
}

func (s *Sampler) Run(ctx context.Context) {
	for {
		next := time.Now().Truncate(s.Every).Add(s.Every)
		if sleepCtx(ctx, time.Until(next)) != nil {
			return
		}
		report := s.sample(ctx, next)
		if report.Host == nil && len(report.Tasks) == 0 {
			continue
		}
		if err := s.Post(ctx, report); err != nil && ctx.Err() == nil {
			s.Log.Warn("posting metrics failed, dropping the sample", "err", err)
		}
	}
}

func (s *Sampler) sample(ctx context.Context, at time.Time) api.MetricsReport {
	report := api.MetricsReport{At: at.UnixMilli(), Server: s.server, Host: s.hostUsage(), Tasks: []api.MetricsTaskEntry{}}
	containers, err := s.Docker.ListSwarmContainers(ctx)
	if err != nil {
		s.Log.Warn("listing containers for metrics failed", "err", err)
		return report
	}
	var running []Container
	for _, c := range containers {
		if _, ok := serviceIDOf(c.Labels); ok && c.State == "running" {
			running = append(running, c)
		}
	}
	stats := make([]ContainerStats, len(running))
	errs := make([]error, len(running))
	var wg sync.WaitGroup
	for i, c := range running {
		wg.Go(func() { stats[i], errs[i] = s.Docker.ContainerStats(ctx, c.ID) })
	}
	wg.Wait()

	seen := map[string]ContainerStats{}
	for i, c := range running {
		if errs[i] != nil {
			continue
		}
		cur := stats[i]
		seen[c.ID] = cur
		prev, ok := s.last[c.ID]
		secs := cur.Read.Sub(prev.Read).Seconds()
		if !ok || secs <= 0 {
			continue
		}
		serviceID, _ := serviceIDOf(c.Labels)
		report.Tasks = append(report.Tasks, api.MetricsTaskEntry{
			ServiceID:     serviceID,
			Task:          c.Labels[labelTaskID],
			CPUCores:      grown(cur.CPUNanos, prev.CPUNanos) / 1e9 / secs,
			MemoryBytes:   int64(cur.MemoryBytes),
			RxBytesPerSec: grown(cur.RxBytes, prev.RxBytes) / secs,
			TxBytesPerSec: grown(cur.TxBytes, prev.TxBytes) / secs,
		})
	}
	s.last = seen
	return report
}

func (s *Sampler) hostUsage() *api.MetricsHost {
	h, err := s.Host()
	if err != nil {
		s.Log.Warn("reading host metrics failed", "err", err)
		return nil
	}
	prev := s.lastHost
	s.lastHost = &h
	if prev == nil || h.CPUTotal <= prev.CPUTotal {
		return nil
	}
	return &api.MetricsHost{
		CPUs:             h.CPUs,
		CPUCores:         grown(h.CPUBusy, prev.CPUBusy) / float64(h.CPUTotal-prev.CPUTotal) * float64(h.CPUs),
		MemoryBytes:      int64(h.MemoryTotalBytes - min(h.MemoryAvailableBytes, h.MemoryTotalBytes)),
		MemoryTotalBytes: int64(h.MemoryTotalBytes),
		DiskBytes:        int64(h.DiskTotalBytes - min(h.DiskFreeBytes, h.DiskTotalBytes)),
		DiskTotalBytes:   int64(h.DiskTotalBytes),
	}
}

func grown(cur, prev uint64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur - prev)
}

func parseProcStat(b []byte) (cpus int, busy, total uint64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	found := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			cpus++
			continue
		}
		if len(fields) < 5 {
			return 0, 0, 0, errors.New("/proc/stat: short cpu line")
		}
		var idle uint64
		for i, f := range fields[1:min(len(fields), 9)] {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, 0, err
			}
			total += n
			if i == 3 || i == 4 {
				idle += n
			}
		}
		busy, found = total-idle, true
	}
	if !found || cpus == 0 {
		return 0, 0, 0, errors.New("/proc/stat: no cpu lines")
	}
	return cpus, busy, total, nil
}

func parseMeminfo(b []byte) (total, available uint64, err error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || (key != "MemTotal" && key != "MemAvailable") {
			continue
		}
		kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		if err != nil {
			return 0, 0, err
		}
		if key == "MemTotal" {
			total = kb * 1024
		} else {
			available = kb * 1024
		}
	}
	if total == 0 {
		return 0, 0, errors.New("/proc/meminfo: no MemTotal")
	}
	return total, available, nil
}
