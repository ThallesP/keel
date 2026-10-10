package app

// The OTLP relay (convex/otlp.ts). Services with tracing on and `keel run` export spans over
// OTLP/HTTP to <site>/otlp/v1/traces with their environment's ingest key; the relay forwards the
// bytes unchanged to the traces dataset of the environment's organization's sink. Apps never hold
// the sink's token, and a new sink takes effect without a redeploy: the route is looked up per
// request. The relay does not read the body, so keel.service_id in the spans is the sender's
// claim (accepted: the organization is the boundary).

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const (
	otlpMaxBody     = 4 * 1024 * 1024
	otlpForwardWait = 30 * time.Second
)

// ensureOTLPKey is the environment's ingest key, made on first use (keel_otlp_ + base64url of 24
// random bytes). Inside the write transaction, so concurrent callers converge on one key.
func (a *App) ensureOTLPKey(tx Tx, ch *Changes, scope EnvScope) (string, error) {
	key, err := tx.OTLPKeyOf(scope.Environment.ID)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrNoRow) {
		return "", err
	}
	key = domain.OTLPKeyPrefix + obsBase64URL(obsRandom(24))
	if err := tx.InsertOTLPKey(scope.Environment.ID, key, a.Now()); err != nil {
		return "", err
	}
	// Every service's tracing view shows the masked key.
	ch.Environment(scope.Org, scope.Environment.ID)
	nodes, err := tx.Nodes(scope.Environment.ID)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		ch.Node(scope.Org, n.EnvironmentID, n.ID)
	}
	return key, nil
}

// otlpRoute is where spans sent with key go: the traces dataset of the key's organization's
// sink. found=false for an unknown key (or one whose environment is gone); sink=nil when the
// organization has nowhere to put traces (no sink, or one from before traces).
func (a *App) otlpRoute(ctx context.Context, key string) (sink *OTLPForward, found bool, err error) {
	err = a.read(ctx, func(tx Tx) error {
		envID, err := tx.OTLPKeyEnvironment(key)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		org, err := tx.OrganizationOfEnvironment(envID)
		if errors.Is(err, ErrNoRow) || (err == nil && org == "") {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		rec, err := orgSinkOf(tx, org)
		if err != nil || rec == nil || rec.Sink.Kind != domain.SinkKindAxiom || rec.Sink.Traces == "" {
			return err
		}
		sink = &OTLPForward{Domain: rec.Sink.Domain, Dataset: rec.Sink.Traces, Token: rec.Sink.Token}
		return nil
	})
	return sink, found, err
}

// OTLPRequest is one POST /otlp/v1/traces as the relay needs it.
type OTLPRequest struct {
	Authorization   string
	ContentType     string
	ContentLength   int64 // -1 when unknown
	ContentEncoding string
	Body            io.Reader
}

// OTLPResponse is what the relay answers. ContentType "" = text/plain.
type OTLPResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

func otlpTextReply(status int, body string) OTLPResponse {
	return OTLPResponse{Status: status, Body: []byte(body)}
}

// RelayTraces handles POST /otlp/v1/traces. Statuses follow OTLP/HTTP: exporters retry 429, 502,
// 503 and 504 and drop on anything else. With no traces dataset to send to, spans are accepted
// and dropped, so an exporter does not log a failure every few seconds.
func (a *App) RelayTraces(ctx context.Context, r OTLPRequest) OTLPResponse {
	key := ""
	if token, ok := strings.CutPrefix(r.Authorization, "Bearer "); ok {
		key = domain.TrimJS(token)
	}
	var sink *OTLPForward
	found := false
	if strings.HasPrefix(key, domain.OTLPKeyPrefix) {
		var err error
		if sink, found, err = a.otlpRoute(ctx, key); err != nil {
			a.Log.Error("otlp: route", "err", err)
			return otlpTextReply(500, "internal error")
		}
	}
	if !found {
		return otlpTextReply(401, "unauthorized")
	}
	ctype, _, _ := strings.Cut(r.ContentType, ";")
	ctype = strings.ToLower(domain.TrimJS(ctype))
	if ctype != "application/x-protobuf" && ctype != "application/json" {
		return otlpTextReply(415, "OTLP over HTTP: application/x-protobuf or application/json")
	}
	if r.ContentLength > otlpMaxBody {
		return otlpTextReply(413, "too large")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, otlpMaxBody+1))
	if err != nil {
		return otlpTextReply(400, "bad request")
	}
	if len(body) > otlpMaxBody {
		return otlpTextReply(413, "too large")
	}
	if sink == nil {
		accepted := OTLPResponse{Status: 200, ContentType: ctype, Body: []byte{}}
		if ctype == "application/json" {
			accepted.Body = []byte("{}")
		}
		return accepted
	}
	fwd := *sink
	fwd.ContentType = ctype
	fwd.ContentEncoding = r.ContentEncoding
	fwd.Body = body
	fctx, cancel := context.WithTimeout(ctx, otlpForwardWait)
	defer cancel()
	res, err := a.Axiom.ForwardTraces(fctx, fwd)
	if err != nil {
		a.Log.Warn("otlp: Axiom unreachable: " + err.Error())
		return otlpTextReply(503, "sink unreachable")
	}
	if res.Status >= 200 && res.Status < 300 {
		ct := res.ContentType
		if ct == "" {
			ct = ctype
		}
		return OTLPResponse{Status: 200, ContentType: ct, Body: res.Body}
	}
	detail := CompactDetail(string(res.Body))
	msg := "otlp: Axiom " + strconv.Itoa(res.Status)
	if detail != "" {
		msg += ": " + detail
	}
	a.Log.Warn(msg)
	switch res.Status {
	case 429, 502, 503, 504:
		return otlpTextReply(res.Status, detail)
	}
	if detail == "" {
		detail = "rejected"
	}
	if res.Status >= 500 {
		return otlpTextReply(503, detail)
	}
	return otlpTextReply(400, detail)
}
