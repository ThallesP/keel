package api

import "github.com/ThallesP/keel/internal/domain"

type Dashboard struct {
	Panels []domain.Panel `json:"panels" doc:"In page order"`
}

type MetricsReport struct {
	At     int64              `json:"at"`
	Server MetricsServer      `json:"server"`
	Host   *MetricsHost       `json:"host"`
	Tasks  []MetricsTaskEntry `json:"tasks"`
}

type MetricsServer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type MetricsHost struct {
	CPUs             int     `json:"cpus"`
	CPUCores         float64 `json:"cpuCores"`
	MemoryBytes      int64   `json:"memoryBytes"`
	MemoryTotalBytes int64   `json:"memoryTotalBytes"`
	DiskBytes        int64   `json:"diskBytes"`
	DiskTotalBytes   int64   `json:"diskTotalBytes"`
}

type MetricsTaskEntry struct {
	ServiceID     string  `json:"serviceId"`
	Task          string  `json:"task"`
	CPUCores      float64 `json:"cpuCores"`
	MemoryBytes   int64   `json:"memoryBytes"`
	RxBytesPerSec float64 `json:"rxBytesPerSec"`
	TxBytesPerSec float64 `json:"txBytesPerSec"`
}
