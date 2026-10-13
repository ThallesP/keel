package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/api"
)

func TestParseProcStat(t *testing.T) {
	stat := "cpu  100 5 50 800 40 3 2 0 9 0\ncpu0 50 2 25 400 20 1 1 0 4 0\ncpu1 50 3 25 400 20 2 1 0 5 0\nintr 123\nctxt 456\n"
	cpus, busy, total, err := parseProcStat([]byte(stat))
	if err != nil || cpus != 2 || total != 1000 || busy != 160 {
		t.Fatalf("parse = %d cpus, busy %d, total %d, %v", cpus, busy, total, err)
	}
	if _, _, _, err := parseProcStat([]byte("intr 1\n")); err == nil {
		t.Fatal("no cpu lines parsed without an error")
	}
}

func TestParseMeminfo(t *testing.T) {
	total, available, err := parseMeminfo([]byte("MemTotal:        2048 kB\nMemFree:          100 kB\nMemAvailable:     512 kB\n"))
	if err != nil || total != 2048*1024 || available != 512*1024 {
		t.Fatalf("parse = %d, %d, %v", total, available, err)
	}
	if _, _, err := parseMeminfo([]byte("MemFree: 1 kB\n")); err == nil {
		t.Fatal("no MemTotal parsed without an error")
	}
}

func TestSamplerRates(t *testing.T) {
	d := newFakeDocker()
	web, old, other := task('a', "n1", "1", "running"), task('b', "n1", "2", "exited"), Container{ID: "c-plain", State: "running", Labels: map[string]string{}}
	d.setContainers(web, old, other)
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	d.setStats(web.ID, ContainerStats{Read: t0, CPUNanos: 1e9, MemoryBytes: 50 << 20, RxBytes: 1000, TxBytes: 100})
	hosts := []Host{
		{CPUs: 4, CPUBusy: 100, CPUTotal: 1000, MemoryTotalBytes: 8 << 30, MemoryAvailableBytes: 6 << 30, DiskTotalBytes: 100 << 30, DiskFreeBytes: 60 << 30},
		{CPUs: 4, CPUBusy: 350, CPUTotal: 2000, MemoryTotalBytes: 8 << 30, MemoryAvailableBytes: 5 << 30, DiskTotalBytes: 100 << 30, DiskFreeBytes: 59 << 30},
	}
	log, _ := testLogger()
	s := &Sampler{Docker: d, Log: log, server: api.MetricsServer{ID: "node-1", Name: "box"}, Host: func() (Host, error) {
		h := hosts[0]
		hosts = hosts[1:]
		return h, nil
	}}

	first := s.sample(context.Background(), t0)
	if first.Host != nil || len(first.Tasks) != 0 {
		t.Fatalf("first sample = %+v, want nothing until a second reading", first)
	}

	d.setStats(web.ID, ContainerStats{Read: t0.Add(15 * time.Second), CPUNanos: 7e9, MemoryBytes: 60 << 20, RxBytes: 16000, TxBytes: 1600})
	second := s.sample(context.Background(), t0.Add(15*time.Second))
	want := api.MetricsReport{
		At:     t0.Add(15 * time.Second).UnixMilli(),
		Server: api.MetricsServer{ID: "node-1", Name: "box"},
		Host:   &api.MetricsHost{CPUs: 4, CPUCores: 1, MemoryBytes: 3 << 30, MemoryTotalBytes: 8 << 30, DiskBytes: 41 << 30, DiskTotalBytes: 100 << 30},
		Tasks:  []api.MetricsTaskEntry{{ServiceID: "n1", Task: "task-a", CPUCores: 0.4, MemoryBytes: 60 << 20, RxBytesPerSec: 1000, TxBytesPerSec: 100}},
	}
	if second.At != want.At || second.Server != want.Server || *second.Host != *want.Host || len(second.Tasks) != 1 || second.Tasks[0] != want.Tasks[0] {
		t.Fatalf("second sample = %+v %+v %+v\nwant %+v %+v %+v", second, second.Host, second.Tasks, want, want.Host, want.Tasks)
	}
}

func TestSamplerSkipsFailedStatsAndHost(t *testing.T) {
	d := newFakeDocker()
	d.setContainers(task('a', "n1", "1", "running"))
	log, logs := testLogger()
	s := &Sampler{Docker: d, Log: log, Host: func() (Host, error) { return Host{}, errors.New("no /proc") }}
	report := s.sample(context.Background(), time.Unix(15, 0))
	if report.Host != nil || len(report.Tasks) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if out := logs.String(); !strings.Contains(out, `msg="reading host metrics failed" err="no /proc"`) {
		t.Fatalf("log = %s", out)
	}
}
