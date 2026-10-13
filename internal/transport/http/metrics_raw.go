package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func (s *Server) registerMetricsRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /worker/metrics", s.workerMetrics)
}

const workerMetricsMaxBody = 1 << 20

func (s *Server) workerMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.workerAuthorized(r) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body api.MetricsReport
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, workerMetricsMaxBody)).Decode(&body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	if err != nil || body.Server.ID == "" || body.At <= 0 {
		writeText(w, http.StatusBadRequest, "bad report")
		return
	}
	if err := s.app.IngestMetrics(r.Context(), metricsReportOf(body)); err != nil {
		s.app.Log.Error("worker metrics", "err", err)
		writeText(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeText(w, http.StatusOK, "ok")
}

func metricsReportOf(b api.MetricsReport) app.MetricsReport {
	report := app.MetricsReport{At: b.At, ServerID: b.Server.ID, Tasks: make([]app.TaskSample, len(b.Tasks))}
	if h := b.Host; h != nil {
		report.Server = &domain.ServerSample{
			ServerID: b.Server.ID, Name: b.Server.Name, At: b.At, CPUs: h.CPUs, CPUCores: h.CPUCores,
			MemoryBytes: h.MemoryBytes, MemoryTotalBytes: h.MemoryTotalBytes,
			DiskBytes: h.DiskBytes, DiskTotalBytes: h.DiskTotalBytes,
		}
	}
	for i, t := range b.Tasks {
		report.Tasks[i] = app.TaskSample{
			NodeID: t.ServiceID, Task: t.Task, CPUCores: t.CPUCores, MemoryBytes: t.MemoryBytes,
			RxBytesPerSec: t.RxBytesPerSec, TxBytesPerSec: t.TxBytesPerSec,
		}
	}
	return report
}
