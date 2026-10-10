package app

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

func CompactText(text string, maxRunes int) string {
	return domain.TruncateRunes(strings.Join(strings.Fields(strings.ToValidUTF8(text, "\uFFFD")), " "), maxRunes)
}

const axiomQueryWindowMs = 30 * 24 * 60 * 60_000

func aplLit(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func aplIn(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = aplLit(v)
	}
	return "in (" + strings.Join(quoted, ", ") + ")"
}

func aplDataset(dataset string) string { return "['" + dataset + "']" }

func (a *App) axiomUntil() int64 { return a.Now() + 60_000 }

func (a *App) axiomRows(ctx context.Context, sink domain.LogSink, apl string, from, to int64) ([]AxiomRow, error) {
	rows, err := a.Axiom.Query(ctx, AxiomTarget{Domain: sink.Domain, Token: sink.Token},
		AxiomQuery{APL: apl, StartTime: time.UnixMilli(from), EndTime: time.UnixMilli(to)})
	if axiomStatus(err) == http.StatusBadRequest && strings.Contains(err.Error(), "invalid field") {
		return nil, nil
	}
	return rows, err
}

func axiomRowsAs[T any](ctx context.Context, a *App, sink domain.LogSink, apl string, from, to int64) ([]T, error) {
	rows, err := a.axiomRows(ctx, sink, apl, from, to)
	if err != nil {
		return nil, err
	}
	return readRows(a.Log, rows, decodeRow[T]), nil
}

func readRows[T any](log *slog.Logger, rows []AxiomRow, read func(AxiomRow) (T, error)) []T {
	out := make([]T, 0, len(rows))
	var firstErr error
	for _, row := range rows {
		v, err := read(row)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			continue
		}
		out = append(out, v)
	}
	if firstErr != nil {
		log.Warn("Axiom: skipped rows Keel cannot read", "skipped", len(rows)-len(out), "rows", len(rows), "err", firstErr)
	}
	return out
}

func decodeRow[T any](row AxiomRow) (T, error) {
	var v T
	b, err := json.Marshal(row)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(b, &v)
	return v, err
}

type axiomTime float64

func (t *axiomTime) UnmarshalJSON(b []byte) error {
	var epoch uint64
	if json.Unmarshal(b, &epoch) == nil {
		*t = epochTime(epoch)
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	if epoch, err := strconv.ParseUint(text, 10, 64); err == nil {
		*t = epochTime(epoch)
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return fmt.Errorf("Axiom time %q: neither RFC 3339 nor epoch", text)
	}
	*t = axiomTime(float64(at.UnixMilli()) + float64(at.Nanosecond()%1_000_000)/1e6)
	return nil
}

func epochTime(epoch uint64) axiomTime {
	switch {
	case epoch > 1e17:
		return axiomTime(float64(epoch/1e6) + float64(epoch%1e6)/1e6)
	case epoch > 1e14:
		return axiomTime(float64(epoch/1e3) + float64(epoch%1e3)/1e3)
	}
	return axiomTime(epoch)
}

var axiomDatasetDescriptions = map[string]string{
	domain.DatasetLogs:   "Keel container logs",
	domain.DatasetTraces: "Keel OpenTelemetry traces",
}

func (a *App) axiomVerify(ctx context.Context, sink domain.LogSink, dataset string) error {
	err := a.Axiom.CreateDataset(ctx, AxiomTarget{Domain: sink.Domain, Token: sink.Token}, "", dataset, axiomDatasetDescriptions[domain.DatasetLogs])
	if status := axiomStatus(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
		return err
	}
	return a.axiomCanQuery(ctx, sink, dataset)
}

func (a *App) axiomCanQuery(ctx context.Context, sink domain.LogSink, dataset string) error {
	_, err := a.axiomRows(ctx, sink, aplDataset(dataset)+" | limit 1", a.Now()-60_000, a.axiomUntil())
	return err
}

type axiomLogRow struct {
	Time      axiomTime `json:"_time"`
	Message   string    `json:"message"`
	Stream    string    `json:"stream"`
	Task      string    `json:"task"`
	Replica   int       `json:"replica"`
	ServiceID string    `json:"service_id"`
}

func (r axiomLogRow) line() domain.ServiceLogLine {
	stream := "stdout"
	if r.Stream == "stderr" {
		stream = "stderr"
	}
	return domain.ServiceLogLine{Time: float64(r.Time), Text: r.Message, Stream: stream, Task: r.Task}
}

func (a *App) axiomTail(ctx context.Context, sink domain.LogSink, serviceID string, n int) (domain.LogTail, error) {
	apl := aplDataset(sink.Dataset) + " | where service_id == " + aplLit(serviceID) +
		" | sort by _time desc | limit " + strconv.Itoa(n) + " | project _time, message, stream, task, replica"
	rows, err := axiomRowsAs[axiomLogRow](ctx, a, sink, apl, a.Now()-axiomQueryWindowMs, a.axiomUntil())
	if err != nil {
		return domain.LogTail{}, err
	}
	lines := make([]domain.ServiceLogLine, len(rows))
	replicas := []domain.LogReplica{}
	seen := map[string]bool{}
	for i, r := range rows {
		lines[len(rows)-1-i] = r.line()
		if r.Task == "" || seen[r.Task] {
			continue
		}
		seen[r.Task] = true
		replicas = append(replicas, domain.LogReplica{Task: r.Task, Slot: r.Replica})
	}
	sortLogReplicas(replicas)
	return domain.LogTail{Source: domain.LogSourceAxiom, Lines: lines, Replicas: replicas}, nil
}

func sortLogReplicas(rs []domain.LogReplica) {
	slices.SortFunc(rs, func(a, b domain.LogReplica) int {
		return cmp.Or(cmp.Compare(a.Slot, b.Slot), strings.Compare(a.Task, b.Task))
	})
}

type linesQuery struct {
	N           int
	Search      string
	From, To    int64
	OldestFirst bool
}

func (a *App) axiomLines(ctx context.Context, sink domain.LogSink, serviceIDs []string, q linesQuery) ([]domain.EnvironmentLogLine, error) {
	if len(serviceIDs) == 0 {
		return []domain.EnvironmentLogLine{}, nil
	}
	where := ""
	if term := strings.TrimSpace(q.Search); term != "" {
		where = " | where message contains " + aplLit(term)
	}
	order := "desc"
	if q.OldestFirst {
		order = "asc"
	}
	apl := aplDataset(sink.Dataset) + " | where service_id " + aplIn(serviceIDs) + where +
		" | sort by _time " + order + " | limit " + strconv.Itoa(q.N) + " | project _time, message, stream, task, service_id"
	rows, err := axiomRowsAs[axiomLogRow](ctx, a, sink, apl, q.From, q.To)
	if err != nil {
		return nil, err
	}
	lines := make([]domain.EnvironmentLogLine, len(rows))
	for i, r := range rows {
		lines[i] = domain.EnvironmentLogLine{ServiceLogLine: r.line(), ServiceID: r.ServiceID}
	}
	if !q.OldestFirst {
		slices.Reverse(lines)
	}
	return lines, nil
}

func (a *App) axiomAuthURL() string {
	return cmp.Or(a.Config.AxiomAuthURL, "https://authorization.axiom.co")
}

func randomBase64URL(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *App) axiomAuthorizeURL(clientID, redirectURI string) (state, verifier, authorizeURL string) {
	verifier, state = randomBase64URL(32), randomBase64URL(16)
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	return state, verifier, a.axiomAuthURL() + "/oauth2/authorize?" + query.Encode()
}

type axiomJWTClaims struct {
	Audience   json.RawMessage `json:"aud"`
	DefaultOrg string          `json:"axiomDefaultOrg"`
}

func axiomClaims(token string) axiomJWTClaims {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return axiomJWTClaims{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return axiomJWTClaims{}
	}
	var claims axiomJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return axiomJWTClaims{}
	}
	return claims
}

func (a *App) axiomOrgs(ctx context.Context, token string) ([]domain.AxiomOrg, error) {
	infos, err := a.Axiom.Orgs(ctx, AxiomTarget{Domain: cmp.Or(a.Config.AxiomAPIURL, domain.AxiomDomains[0]), Token: token})
	if err != nil {
		a.Log.Warn("axiom: API rejected the sign-in token", "aud", string(axiomClaims(token).Audience), "err", err)
		return nil, fmt.Errorf("%w (Axiom API rejected the sign-in token)", err)
	}
	orgs := make([]domain.AxiomOrg, len(infos))
	for i, o := range infos {
		region := domain.AxiomDomains[0]
		if strings.Contains(o.Edge, "eu-") {
			region = domain.AxiomDomains[1]
		}
		orgs[i] = domain.AxiomOrg{ID: o.ID, Name: o.Name, MaxDatasets: o.MaxDatasets, Domain: cmp.Or(a.Config.AxiomAPIURL, region)}
	}
	return orgs, nil
}

func (a *App) axiomProvision(ctx context.Context, token string, org domain.AxiomOrg, label string) (domain.LogSink, error) {
	t := AxiomTarget{Domain: org.Domain, Token: token}
	existing, err := a.Axiom.Datasets(ctx, t, org.ID)
	if err != nil {
		return domain.LogSink{}, fmt.Errorf("Listing datasets: %w", err)
	}
	have := map[string]bool{}
	var own []string
	for _, d := range existing {
		have[d.Name] = true
		if !d.Shared {
			own = append(own, d.Name)
		}
	}
	var missing []string
	for _, name := range []string{domain.DatasetLogs, domain.DatasetTraces} {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	for i, name := range missing {
		if err := a.Axiom.CreateDataset(ctx, t, org.ID, name, axiomDatasetDescriptions[name]); err != nil {
			return domain.LogSink{}, datasetCapError(org, own, missing[i:], err)
		}
		own = append(own, name)
	}
	minted, err := a.Axiom.MintToken(ctx, t, org.ID, AxiomTokenRequest{
		Name:        label,
		Description: "Keel: logs and traces go in, the control plane reads them back",
		Datasets:    []string{domain.DatasetLogs, domain.DatasetTraces},
	})
	if err != nil {
		return domain.LogSink{}, fmt.Errorf("Minting the ingest token: %w", err)
	}
	if minted == "" {
		return domain.LogSink{}, errors.New("Axiom did not return a token")
	}
	return domain.LogSink{
		Kind: domain.SinkKindAxiom, Domain: org.Domain, Dataset: domain.DatasetLogs, Traces: domain.DatasetTraces, Token: minted, Org: org.Name,
	}, nil
}

func datasetCapError(org domain.AxiomOrg, own, left []string, err error) error {
	if org.MaxDatasets > 0 && axiomStatus(err) == http.StatusBadRequest && len(own) >= org.MaxDatasets {
		return fmt.Errorf("%s is at its Axiom plan's limit of %d datasets (%s). Keel needs %s: delete %d in Axiom or pick another org. (%w)",
			org.Name, org.MaxDatasets, strings.Join(own, ", "), strings.Join(left, " and "), len(own)+len(left)-org.MaxDatasets, err)
	}
	return fmt.Errorf("Creating %s: %w", left[0], err)
}
