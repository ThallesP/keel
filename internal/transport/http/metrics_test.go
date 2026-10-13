package http

import (
	"strings"
	"testing"
)

func TestWorkerMetricsRoute(t *testing.T) {
	h := newObsHarness(t)
	report := `{"at":1791460800000,"server":{"id":"s1","name":"box"},` +
		`"host":{"cpus":2,"cpuCores":0.5,"memoryBytes":1024,"memoryTotalBytes":4096,"diskBytes":10,"diskTotalBytes":100},` +
		`"tasks":[{"serviceId":"api","task":"t1","cpuCores":0.25,"memoryBytes":512,"rxBytesPerSec":3,"txBytesPerSec":4}]}`
	if r := h.do(t, "", "POST", "/worker/metrics", report, "Authorization", "Bearer wrong"); r.status != 401 || r.body != "unauthorized" {
		t.Fatalf("bad token: %d %s", r.status, r.body)
	}
	for _, body := range []string{`[]`, `{"at":1,"server":{"id":""}}`, `{"server":{"id":"s1"}}`} {
		if r := h.do(t, "", "POST", "/worker/metrics", body, "Authorization", "Bearer worker-secret"); r.status != 400 || r.body != "bad report" {
			t.Fatalf("%s: %d %s", body, r.status, r.body)
		}
	}
	big := `{"at":1,"server":{"id":"s1","name":"` + strings.Repeat("x", workerMetricsMaxBody) + `"}}`
	if r := h.do(t, "", "POST", "/worker/metrics", big, "Authorization", "Bearer worker-secret"); r.status != 413 {
		t.Fatalf("too large: %d %s", r.status, r.body)
	}
	if r := h.do(t, "", "POST", "/worker/metrics", report, "Authorization", "Bearer worker-secret"); r.status != 200 || r.body != "ok" {
		t.Fatalf("report: %d %s", r.status, r.body)
	}
	r := h.do(t, "member", "GET", "/api/environments/env/metrics?range=15m", "")
	if r.status != 200 || !strings.Contains(r.body, `"nodeId":"api"`) || !strings.Contains(r.body, `"cpuCores":0.25`) ||
		!strings.Contains(r.body, `"servers":[{"id":"s1","name":"box","at":1791460800000,"cpus":2,"cpuCores":0.5,"memoryBytes":1024,"memoryTotalBytes":4096,"diskBytes":10,"diskTotalBytes":100,"nodeIds":["api"]}]`) {
		t.Fatalf("overview: %d %s", r.status, r.body)
	}
	r = h.do(t, "member", "PUT", "/api/environments/env/dashboard", `{"panels":[{"id":"a","kind":"cpu","nodeId":"api","search":"","wide":true}]}`)
	if r.status != 200 || r.body != `{"panels":[{"id":"a","kind":"cpu","nodeId":"api","search":"","wide":true}]}` {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
}
