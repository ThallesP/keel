package app

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

type OTLPRequest struct {
	Authorization   string
	ContentType     string
	ContentLength   int64
	ContentEncoding string
	Body            io.Reader
}

type OTLPResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

func otlpTextReply(status int, body string) OTLPResponse {
	return OTLPResponse{Status: status, Body: []byte(body)}
}

func (a *App) RelayTraces(ctx context.Context, r OTLPRequest) OTLPResponse {
	key := ""
	if token, ok := strings.CutPrefix(r.Authorization, "Bearer "); ok {
		key = strings.TrimSpace(token)
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
	ctype = strings.ToLower(strings.TrimSpace(ctype))
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
