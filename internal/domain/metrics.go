package domain

import (
	"slices"
	"time"
	"unicode/utf8"
)

const (
	MetricsInterval  = 15 * time.Second
	MetricsRetention = 7 * 24 * time.Hour
	PlacementWindow  = time.Minute
)

type ServiceSample struct {
	NodeID        string
	Task          string
	ServerID      string
	At            int64
	CPUCores      float64
	MemoryBytes   int64
	RxBytesPerSec float64
	TxBytesPerSec float64
}

type ServerSample struct {
	ServerID         string
	Name             string
	At               int64
	CPUs             int
	CPUCores         float64
	MemoryBytes      int64
	MemoryTotalBytes int64
	DiskBytes        int64
	DiskTotalBytes   int64
}

type MetricsBucket struct {
	Time          int64    `json:"time"`
	CPUCores      *float64 `json:"cpuCores" nullable:"true" doc:"Average cores in use, every replica summed; null when nothing ran"`
	MemoryBytes   *float64 `json:"memoryBytes" nullable:"true" doc:"Peak memory in use, every replica summed"`
	RxBytesPerSec *float64 `json:"rxBytesPerSec" nullable:"true" doc:"Average bytes received per second"`
	TxBytesPerSec *float64 `json:"txBytesPerSec" nullable:"true" doc:"Average bytes sent per second"`
}

type ServiceMetrics struct {
	NodeID  string          `json:"nodeId"`
	Buckets []MetricsBucket `json:"buckets" doc:"Every bucket, oldest first, empty ones included"`
}

type ServerUsage struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	At               int64    `json:"at" doc:"When this sample was taken"`
	CPUs             int      `json:"cpus"`
	CPUCores         float64  `json:"cpuCores" doc:"Cores in use on the whole server"`
	MemoryBytes      int64    `json:"memoryBytes"`
	MemoryTotalBytes int64    `json:"memoryTotalBytes"`
	DiskBytes        int64    `json:"diskBytes" doc:"Used on the disk that holds Docker's data"`
	DiskTotalBytes   int64    `json:"diskTotalBytes"`
	NodeIDs          []string `json:"nodeIds" doc:"The environment's services running on it"`
}

type DeployMark struct {
	ID      string           `json:"id"`
	At      int64            `json:"at"`
	Message string           `json:"message"`
	Status  DeploymentStatus `json:"status" enum:"running,success,failed"`
}

type MetricsOverview struct {
	From        int64            `json:"from"`
	To          int64            `json:"to"`
	BucketMs    int64            `json:"bucketMs"`
	Total       []MetricsBucket  `json:"total" doc:"Every service in scope summed per sample, then per bucket; empty buckets included"`
	Services    []ServiceMetrics `json:"services" doc:"Services with at least one sample in range"`
	Servers     []ServerUsage    `json:"servers" doc:"Servers running the environment's services in the last minute"`
	Deployments []DeployMark     `json:"deployments" doc:"Ships started in range, oldest first"`
}

type PanelKind string

const (
	PanelCPU      PanelKind = "cpu"
	PanelMemory   PanelKind = "memory"
	PanelNetwork  PanelKind = "network"
	PanelRequests PanelKind = "requests"
	PanelLatency  PanelKind = "latency"
	PanelLogs     PanelKind = "logs"
	PanelServers  PanelKind = "servers"
)

var panelKinds = []PanelKind{PanelCPU, PanelMemory, PanelNetwork, PanelRequests, PanelLatency, PanelLogs, PanelServers}

type Panel struct {
	ID     string    `json:"id" maxLength:"32"`
	Kind   PanelKind `json:"kind" enum:"cpu,memory,network,requests,latency,logs,servers"`
	NodeID string    `json:"nodeId" doc:"Only this service; empty for every service"`
	Search string    `json:"search" doc:"logs: only lines containing this"`
	Wide   bool      `json:"wide" doc:"Spans the page's two columns"`
}

const MaxPanels = 24

func DefaultPanels() []Panel {
	return []Panel{
		{ID: "cpu", Kind: PanelCPU},
		{ID: "memory", Kind: PanelMemory},
		{ID: "requests", Kind: PanelRequests},
		{ID: "latency", Kind: PanelLatency},
		{ID: "network", Kind: PanelNetwork},
		{ID: "servers", Kind: PanelServers},
		{ID: "errors", Kind: PanelLogs, Search: "error", Wide: true},
	}
}

func ValidatePanels(panels []Panel, nodeIDs []string) error {
	if len(panels) > MaxPanels {
		return Invalid("At most %d panels", MaxPanels)
	}
	seen := map[string]bool{}
	for _, p := range panels {
		if p.ID == "" || seen[p.ID] {
			return Invalid("Every panel needs its own id")
		}
		seen[p.ID] = true
		if !slices.Contains(panelKinds, p.Kind) {
			return Invalid("Unknown panel kind %q", p.Kind)
		}
		if p.NodeID != "" && !slices.Contains(nodeIDs, p.NodeID) {
			return E(CodeServiceNotFound, MsgNodeNotFound)
		}
		if utf8.RuneCountInString(p.Search) > 200 {
			return Invalid("Search: at most 200 characters")
		}
	}
	return nil
}

func PanelsFor(panels []Panel, nodeIDs []string) []Panel {
	out := slices.Clone(panels)
	for i, p := range out {
		if !slices.Contains(nodeIDs, p.NodeID) {
			out[i].NodeID = ""
		}
	}
	return out
}
