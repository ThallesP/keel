package app

import "github.com/ThallesP/keel/internal/domain"

type MetricsTx interface {
	NodeExists(id string) (bool, error)
	InsertServiceSample(s domain.ServiceSample) error
	InsertServerSample(s domain.ServerSample) error
	ServiceBuckets(q BucketQuery) ([]ServiceBucketRow, error)
	TotalBuckets(q BucketQuery) ([]ServiceBucketRow, error)
	RecentPlacements(environmentID string, since int64) ([]Placement, error)
	LatestServerSample(serverID string, since int64) (domain.ServerSample, error)
	DeployMarks(environmentID string, from, to int64) ([]domain.DeployMark, error)
	RollupMetrics(since int64) error
	PruneMetrics(rawBefore, hourlyBefore int64) error

	DashboardOf(environmentID string) ([]domain.Panel, error)
	SaveDashboard(environmentID string, panels []domain.Panel, now int64) error
}

type BucketQuery struct {
	EnvironmentID string
	NodeID        string
	From          int64
	To            int64
	BinMs         int64
	Hourly        bool
}

type ServiceBucketRow struct {
	NodeID        string
	Bucket        int64
	CPUCores      float64
	MemoryBytes   float64
	RxBytesPerSec float64
	TxBytesPerSec float64
}

type Placement struct {
	ServerID string
	NodeID   string
}

type MetricsReport struct {
	At       int64
	ServerID string
	Server   *domain.ServerSample
	Tasks    []TaskSample
}

type TaskSample struct {
	NodeID        string
	Task          string
	CPUCores      float64
	MemoryBytes   int64
	RxBytesPerSec float64
	TxBytesPerSec float64
}
