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
	Domain  string
	Dataset string
	Token   string
}

func (c axiomCfg) target() AxiomTarget { return AxiomTarget{Domain: c.Domain, Token: c.Token} }

func axiomLogsCfg(s domain.LogSink) axiomCfg {
	return axiomCfg{Domain: s.Domain, Dataset: s.Dataset, Token: s.Token}
}

func AxiomBaseURL(domain string) string {
	if !strings.Contains(domain, "://") {
		domain = "https://" + domain
	}
	return strings.TrimRight(domain, "/")
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

const (
	axiomQueryWindowMs = 30 * 24 * 60 * 60_000
	axiomUntilSlackMs  = 60_000
)

func aplLit(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func aplDataset(dataset string) string { return "['" + dataset + "']" }

func aplTime(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func (a *App) axiomUntil() float64 { return float64(a.Now() + axiomUntilSlackMs) }

func (a *App) axiomQuery(ctx context.Context, cfg axiomCfg, apl string, from, to float64) ([]AxiomRow, error) {
	return a.Axiom.Query(ctx, cfg.target(), AxiomQuery{APL: apl, StartTime: aplTime(from), EndTime: aplTime(to)})
}

func (a *App) axiomRows(ctx context.Context, cfg axiomCfg, apl string, from, to float64) ([]AxiomRow, error) {
	rows, err := a.axiomQuery(ctx, cfg, apl, from, to)
	if axiomStatus(err) == http.StatusBadRequest && strings.Contains(err.Error(), "invalid field") {
		return []AxiomRow{}, nil
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
	var skipped []error
	for _, row := range rows {
		v, err := read(row)
		if err != nil {
			skipped = append(skipped, err)
			continue
		}
		out = append(out, v)
	}
	if len(skipped) > 0 {
		log.Warn("Axiom: skipped rows Keel cannot read", "skipped", len(skipped), "rows", len(rows), "err", skipped[0])
	}
	return out
}

func decodeRow[T any](row AxiomRow) (T, error) {
	var v T
	b, err := json.Marshal(row)
	if err != nil {
		return v, fmt.Errorf("Axiom row: %w", err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return v, fmt.Errorf("Axiom row: %w", err)
	}
	return v, nil
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
	if !domain.ValidDataset(cfg.Dataset) {
		return errors.New("Dataset name: letters, digits, - _ . only")
	}
	err := a.Axiom.CreateDataset(ctx, cfg.target(), "", cfg.Dataset, axiomDatasetDescriptions[domain.DatasetLogs])
	if status := axiomStatus(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
		return err
	}
	return a.axiomCanQuery(ctx, cfg)
}

func (a *App) axiomCanQuery(ctx context.Context, cfg axiomCfg) error {
	_, err := a.axiomQuery(ctx, cfg, aplDataset(cfg.Dataset)+" | limit 1", float64(a.Now()-60_000), a.axiomUntil())
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
	slices.SortStableFunc(rs, func(a, b domain.LogReplica) int {
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
	ids := make([]string, len(serviceIDs))
	for i, id := range serviceIDs {
		ids[i] = aplLit(id)
	}
	where := ""
	if term := strings.TrimSpace(q.Search); term != "" {
		where = " | where message contains " + aplLit(term)
	}
	order := "desc"
	if q.OldestFirst {
		order = "asc"
	}
	apl := aplDataset(cfg.Dataset) + " | where service_id in (" + strings.Join(ids, ", ") + ")" + where +
		" | sort by _time " + order + " | limit " + strconv.Itoa(q.N) + " | project _time, message, stream, task, service_id"
	rows, err := axiomRowsAs[axiomLogRow](ctx, a, cfg, apl, q.From, q.To)
	if err != nil {
		return nil, err
	}
	lines := make([]domain.EnvironmentLogLine, len(rows))
	for i, r := range rows {
		at := i
		if !q.OldestFirst {
			at = len(rows) - 1 - i
		}
		lines[at] = domain.EnvironmentLogLine{ServiceLogLine: r.line(), ServiceID: r.ServiceID}
	}
	return lines, nil
}

func (a *App) axiomAuthURL() string {
	u := "https://authorization.axiom.co"
	if a.Config.AllowLocalSinks && a.Config.AxiomAuthURL != "" {
		u = a.Config.AxiomAuthURL
	}
	return strings.TrimRight(u, "/")
}

func (a *App) axiomAPIOverride() string {
	if a.Config.AllowLocalSinks {
		return a.Config.AxiomAPIURL
	}
	return ""
}

func obsBase64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func obsRandom(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func axiomPKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return obsBase64URL(sum[:])
}

func (a *App) axiomAuthorizeURL(clientID, redirectURI string) (state, verifier, authorizeURL string) {
	verifier = obsBase64URL(obsRandom(32))
	state = obsBase64URL(obsRandom(16))
	query := url.Values{
		"client_id":             {clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email"},
		"state":                 {state},
		"code_challenge":        {axiomPKCEChallenge(verifier)},
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
	if claims.Audience == nil {
		return "null"
	}
	return string(claims.Audience)
}

func axiomChosenOrg(token string) string {
	claims, _ := axiomClaims(token)
	return claims.DefaultOrg
}

func (a *App) axiomOrgs(ctx context.Context, token string) ([]domain.AxiomOrg, error) {
	override := a.axiomAPIOverride()
	host := domain.AxiomDomains[0]
	if override != "" {
		host = override
	}
	infos, err := a.Axiom.Orgs(ctx, AxiomTarget{Domain: host, Token: token})
	if err != nil {
		return nil, errors.New(err.Error() + " (Axiom API rejected the sign-in token, aud " + axiomJWTAudience(token) + ")")
	}
	orgs := make([]domain.AxiomOrg, len(infos))
	for i, o := range infos {
		d := domain.AxiomDomains[0]
		if strings.Contains(o.Edge, "eu-") {
			d = domain.AxiomDomains[1]
		}
		orgs[i] = domain.AxiomOrg{ID: o.ID, Name: o.Name, MaxDatasets: o.MaxDatasets, Domain: cmp.Or(override, d)}
	}
	return orgs, nil
}

func (a *App) axiomProvision(ctx context.Context, token string, org domain.AxiomOrg, label string) (domain.LogSink, error) {
	t := AxiomTarget{Domain: org.Domain, Token: token}
	existing, err := a.Axiom.Datasets(ctx, t, org.ID)
	if err != nil {
		return domain.LogSink{}, errors.New("Listing datasets: " + err.Error())
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
		return domain.LogSink{}, errors.New("Minting the ingest token: " + err.Error())
	}
	if minted == "" {
		return domain.LogSink{}, errors.New("Axiom did not return a token")
	}
	return domain.LogSink{Kind: domain.SinkKindAxiom, Domain: org.Domain, Dataset: domain.DatasetLogs, Traces: domain.DatasetTraces, Token: minted}, nil
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
