package app

import (
	"cmp"
	"context"
	"errors"
	"io"
	"mime"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const otlpMaxBody = 4 * 1024 * 1024

func (a *App) ensureOTLPKey(tx Tx, ch *Changes, scope EnvScope) (string, error) {
	key, err := tx.OTLPKeyOf(scope.Environment.ID)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ErrNoRow) {
		return "", err
	}
	key = domain.OTLPKeyPrefix + randomBase64URL(24)
	if err := tx.InsertOTLPKey(scope.Environment.ID, key, a.Now()); err != nil {
		return "", err
	}
	ch.Environment(scope.Project.OrganizationID, scope.Environment.ID)
	nodes, err := tx.Nodes(scope.Environment.ID)
	if err != nil {
		return "", err
	}
	for _, n := range nodes {
		ch.Node(scope.Project.OrganizationID, n.EnvironmentID, n.ID)
	}
	return key, nil
}

func (a *App) otlpRoute(ctx context.Context, key string) (domain.LogSink, error) {
	var sink domain.LogSink
	err := a.read(ctx, func(tx Tx) error {
		org, err := tx.OTLPKeyOrganization(key)
		if err != nil {
			return err
		}
		sink, _, err = orgSinkOf(tx, org)
		return err
	})
	return sink, err
}

type OTLPRequest struct {
	Key             string
	ContentType     string
	ContentLength   int64
	ContentEncoding string
	Body            io.Reader
}

func otlpText(status int, body string) HTTPReply {
	return HTTPReply{Status: status, Body: []byte(body)}
}

func (a *App) RelayTraces(ctx context.Context, r OTLPRequest) HTTPReply {
	if !strings.HasPrefix(r.Key, domain.OTLPKeyPrefix) {
		return otlpText(401, "unauthorized")
	}
	sink, err := a.otlpRoute(ctx, r.Key)
	if errors.Is(err, ErrNoRow) {
		return otlpText(401, "unauthorized")
	}
	if err != nil {
		a.Log.Error("otlp: route", "err", err)
		return otlpText(500, "internal error")
	}
	ctype, _, _ := mime.ParseMediaType(r.ContentType)
	if ctype != "application/x-protobuf" && ctype != "application/json" {
		return otlpText(415, "OTLP over HTTP: application/x-protobuf or application/json")
	}
	if r.ContentLength > otlpMaxBody {
		return otlpText(413, "too large")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, otlpMaxBody+1))
	if err != nil {
		return otlpText(400, "bad request")
	}
	if len(body) > otlpMaxBody {
		return otlpText(413, "too large")
	}
	if sink.Traces == "" {
		if ctype == "application/json" {
			return HTTPReply{Status: 200, ContentType: ctype, Body: []byte("{}")}
		}
		return HTTPReply{Status: 200, ContentType: ctype}
	}
	fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := a.Axiom.ForwardTraces(fctx, OTLPForward{
		Domain: sink.Domain, Token: sink.Token, Dataset: sink.Traces, ContentType: ctype, ContentEncoding: r.ContentEncoding, Body: body,
	})
	if err != nil {
		a.Log.Warn("otlp: Axiom unreachable", "err", err)
		return otlpText(503, "sink unreachable")
	}
	if res.Status >= 200 && res.Status < 300 {
		return HTTPReply{Status: 200, ContentType: cmp.Or(res.ContentType, ctype), Body: res.Body}
	}
	detail := CompactText(string(res.Body), 200)
	a.Log.Warn("otlp: Axiom rejected spans", "status", res.Status, "detail", detail)
	if slices.Contains([]int{429, 502, 503, 504}, res.Status) {
		return otlpText(res.Status, detail)
	}
	detail = cmp.Or(detail, "rejected")
	if res.Status >= 500 {
		return otlpText(503, detail)
	}
	return otlpText(400, detail)
}
