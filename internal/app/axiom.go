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

type axiomCfg struct {
	AxiomTarget
	Dataset string
}

func axiomCfgOf(s domain.LogSink, dataset string) axiomCfg {
	return axiomCfg{AxiomTarget{Domain: s.Domain, Token: s.Token}, dataset}
}

func CompactDetail(body string) string { return compactText(body, 200) }

func compactText(text string, maxRunes int) string {
	return truncateRunes(strings.Join(strings.Fields(strings.ToValidUTF8(text, "\uFFFD")), " "), maxRunes)
}

func truncateRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
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

func aplTime(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func (a *App) axiomUntil() float64 { return float64(a.Now() + 60_000) }

func (a *App) axiomRows(ctx context.Context, cfg axiomCfg, apl string, from, to float64) ([]AxiomRow, error) {
	rows, err := a.Axiom.Query(ctx, cfg.AxiomTarget, AxiomQuery{APL: apl, StartTime: aplTime(from), EndTime: aplTime(to)})
	if axiomStatus(err) == http.StatusBadRequest && strings.Contains(err.Error(), "invalid field") {
		return nil, nil
	}
	return rows, err
}

func axiomRowsAs[T any](ctx context.Context, a *App, cfg axiomCfg, apl string, from, to float64) ([]T, error) {
	rows, err := a.axiomRows(ctx, cfg, apl, from, to)
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

func (a *App) axiomVerify(ctx context.Context, cfg axiomCfg) error {
	err := a.Axiom.CreateDataset(ctx, cfg.AxiomTarget, "", cfg.Dataset, axiomDatasetDescriptions[domain.DatasetLogs])
	if status := axiomStatus(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
		return err
	}
	return a.axiomCanQuery(ctx, cfg)
}

func (a *App) axiomCanQuery(ctx context.Context, cfg axiomCfg) error {
	_, err := a.axiomRows(ctx, cfg, aplDataset(cfg.Dataset)+" | limit 1", float64(a.Now()-60_000), a.axiomUntil())
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

func (a *App) axiomTail(ctx context.Context, cfg axiomCfg, serviceID string, n int) (domain.LogTail, error) {
	apl := aplDataset(cfg.Dataset) + " | where service_id == " + aplLit(serviceID) +
		" | sort by _time desc | limit " + strconv.Itoa(n) + " | project _time, message, stream, task, replica"
	rows, err := axiomRowsAs[axiomLogRow](ctx, a, cfg, apl, float64(a.Now()-axiomQueryWindowMs), a.axiomUntil())
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
	From, To    float64
	OldestFirst bool
}

func (a *App) axiomLines(ctx context.Context, cfg axiomCfg, serviceIDs []string, q linesQuery) ([]domain.EnvironmentLogLine, error) {
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
	apl := aplDataset(cfg.Dataset) + " | where service_id " + aplIn(serviceIDs) + where +
		" | sort by _time " + order + " | limit " + strconv.Itoa(q.N) + " | project _time, message, stream, task, service_id"
	rows, err := axiomRowsAs[axiomLogRow](ctx, a, cfg, apl, q.From, q.To)
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
	if a.Config.AllowLocalSinks && a.Config.AxiomAuthURL != "" {
		return strings.TrimRight(a.Config.AxiomAuthURL, "/")
	}
	return "https://authorization.axiom.co"
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

func axiomClaims(token string) (axiomJWTClaims, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return axiomJWTClaims{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return axiomJWTClaims{}, false
	}
	var claims axiomJWTClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return axiomJWTClaims{}, false
	}
	return claims, true
}

func axiomJWTAudience(token string) string {
	claims, ok := axiomClaims(token)
	if !ok {
		return "(not a JWT)"
	}
	return cmp.Or(string(claims.Audience), "null")
}

func axiomChosenOrg(token string) string {
	claims, _ := axiomClaims(token)
	return claims.DefaultOrg
}

func (a *App) axiomOrgs(ctx context.Context, token string) ([]domain.AxiomOrg, error) {
	override := ""
	if a.Config.AllowLocalSinks {
		override = a.Config.AxiomAPIURL
	}
	infos, err := a.Axiom.Orgs(ctx, AxiomTarget{Domain: cmp.Or(override, domain.AxiomDomains[0]), Token: token})
	if err != nil {
		return nil, fmt.Errorf("%w (Axiom API rejected the sign-in token, aud %s)", err, axiomJWTAudience(token))
	}
	orgs := make([]domain.AxiomOrg, len(infos))
	for i, o := range infos {
		region := domain.AxiomDomains[0]
		if strings.Contains(o.Edge, "eu-") {
			region = domain.AxiomDomains[1]
		}
		orgs[i] = domain.AxiomOrg{ID: o.ID, Name: o.Name, MaxDatasets: o.MaxDatasets, Domain: cmp.Or(override, region)}
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
			return domain.LogSink{}, errors.New(datasetCapMessage(org, own, missing[i:], err))
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

func datasetCapMessage(org domain.AxiomOrg, own, left []string, err error) string {
	msg := err.Error()
	if org.MaxDatasets > 0 && axiomStatus(err) == http.StatusBadRequest && len(own) >= org.MaxDatasets {
		return org.Name + " is at its Axiom plan's limit of " + strconv.Itoa(org.MaxDatasets) + " datasets (" +
			strings.Join(own, ", ") + "). Keel needs " + strings.Join(left, " and ") + ": delete " +
			strconv.Itoa(len(own)+len(left)-org.MaxDatasets) + " in Axiom or pick another org. (" + msg + ")"
	}
	return "Creating " + left[0] + ": " + msg
}
