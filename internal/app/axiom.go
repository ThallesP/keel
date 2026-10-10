package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

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

const (
	axiomQueryWindowMs = 30 * 24 * 60 * 60_000
	axiomUntilSlackMs  = 60_000
)

func aplLit(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func aplDataset(dataset string) string { return "['" + dataset + "']" }

func (a *App) axiomQuery(ctx context.Context, cfg axiomCfg, apl string, since float64, until *float64) ([]*JSONObject, error) {
	end := float64(a.Now() + axiomUntilSlackMs)
	if until != nil {
		end = *until
	}
	return a.Axiom.Query(ctx, cfg.target(), AxiomQuery{APL: apl, StartTime: jsISOTime(since), EndTime: jsISOTime(end)})
}

var axiomInvalidFieldRE = regexp.MustCompile(`Axiom 400.*invalid field`)

func (a *App) axiomRows(ctx context.Context, cfg axiomCfg, apl string, since, until *float64) ([]*JSONObject, error) {
	from := float64(a.Now() - axiomQueryWindowMs)
	if since != nil {
		from = *since
	}
	rows, err := a.axiomQuery(ctx, cfg, apl, from, until)
	if err != nil {
		if axiomInvalidFieldRE.MatchString(err.Error()) {
			return []*JSONObject{}, nil
		}
		return nil, err
	}
	return rows, nil
}

func obsF64(x float64) *float64 { return &x }

var (
	axiomExistsRE  = regexp.MustCompile(`(?i)exists|409`)
	axiomAuthErrRE = regexp.MustCompile(`40[13]`)
)

func (a *App) axiomVerify(ctx context.Context, cfg axiomCfg) error {
	if !domain.ValidDataset(cfg.Dataset) {
		return errors.New("Dataset name: letters, digits, - _ . only")
	}
	err := a.Axiom.CreateDataset(ctx, cfg.target(), "", cfg.Dataset, "Keel container logs")
	if err != nil && !axiomExistsRE.MatchString(err.Error()) && axiomAuthErrRE.MatchString(err.Error()) {
		return err
	}
	return a.axiomCanQuery(ctx, cfg)
}

func (a *App) axiomCanQuery(ctx context.Context, cfg axiomCfg) error {
	_, err := a.axiomQuery(ctx, cfg, aplDataset(cfg.Dataset)+" | limit 1", float64(a.Now()-60_000), nil)
	return err
}

func logStreamOf(v any) string {
	if v == "stderr" {
		return "stderr"
	}
	return "stdout"
}

func (a *App) axiomTail(ctx context.Context, cfg axiomCfg, serviceID string, n int) (domain.LogTail, error) {
	apl := aplDataset(cfg.Dataset) + " | where service_id == " + aplLit(serviceID) +
		" | sort by _time desc | limit " + strconv.Itoa(n) + " | project _time, message, stream, task, replica"
	rows, err := a.axiomRows(ctx, cfg, apl, nil, nil)
	if err != nil {
		return domain.LogTail{}, err
	}
	lines := make([]domain.ServiceLogLine, len(rows))
	for i, r := range rows {
		t, _ := r.Get("_time")
		ms, _ := jsDateParse(jsString(t))
		msg, _ := r.Get("message")
		stream, _ := r.Get("stream")
		task, _ := r.Get("task")
		lines[len(rows)-1-i] = domain.ServiceLogLine{Time: ms, Text: jsString(msg), Stream: logStreamOf(stream), Task: jsString(task)}
	}
	replicas := []domain.LogReplica{}
	seen := map[string]bool{}
	for _, r := range rows {
		tv, _ := r.Get("task")
		task := jsString(tv)
		if task == "" || seen[task] {
			continue
		}
		seen[task] = true
		rv, _ := r.Get("replica")
		replicas = append(replicas, domain.LogReplica{Task: task, Slot: int(jsNum(rv)), State: ""})
	}
	sortLogReplicas(replicas)
	return domain.LogTail{Source: domain.LogSourceAxiom, Lines: lines, Replicas: replicas}, nil
}

func sortLogReplicas(rs []domain.LogReplica) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Slot != rs[j].Slot {
			return rs[i].Slot < rs[j].Slot
		}
		return localeCompare(rs[i].Task, rs[j].Task) < 0
	})
}

type linesQuery struct {
	N           int
	Search      string
	From, To    *float64
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
	if term := domain.TrimJS(q.Search); term != "" {
		where = " | where message contains " + aplLit(term)
	}
	order := "desc"
	if q.OldestFirst {
		order = "asc"
	}
	apl := aplDataset(cfg.Dataset) + " | where service_id in (" + strings.Join(ids, ", ") + ")" + where +
		" | sort by _time " + order + " | limit " + strconv.Itoa(q.N) + " | project _time, message, stream, task, service_id"
	rows, err := a.axiomRows(ctx, cfg, apl, q.From, q.To)
	if err != nil {
		return nil, err
	}
	lines := make([]domain.EnvironmentLogLine, len(rows))
	for i, r := range rows {
		t, _ := r.Get("_time")
		msg, _ := r.Get("message")
		stream, _ := r.Get("stream")
		task, _ := r.Get("task")
		sid, _ := r.Get("service_id")
		at := i
		if !q.OldestFirst {
			at = len(rows) - 1 - i
		}
		lines[at] = domain.EnvironmentLogLine{
			ServiceLogLine: domain.ServiceLogLine{Time: axiomPreciseTime(t), Text: jsString(msg), Stream: logStreamOf(stream), Task: jsString(task)},
			ServiceID:      jsString(sid),
		}
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

func (a *App) axiomAuthorizeURL(clientID, redirectURI string) (state, verifier, url string) {
	verifier = obsBase64URL(obsRandom(32))
	state = obsBase64URL(obsRandom(16))
	url = a.axiomAuthURL() + "/oauth2/authorize?" + jsFormEncode([][2]string{
		{"client_id", clientID},
		{"response_type", "code"},
		{"redirect_uri", redirectURI},
		{"scope", "openid profile email"},
		{"state", state},
		{"code_challenge", axiomPKCEChallenge(verifier)},
		{"code_challenge_method", "S256"},
	})
	return state, verifier, url
}

func axiomJWTClaims(token string) *JSONObject {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := strings.TrimRight(parts[1], "=")
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(seg); err != nil {
			return nil
		}
	}
	v, err := DecodeJSON(b)
	if err != nil {
		return nil
	}
	obj, _ := v.(*JSONObject)
	return obj
}

func axiomJWTAudience(token string) string {
	c := axiomJWTClaims(token)
	if c == nil {
		return "(not a JWT)"
	}
	aud, _ := c.Get("aud")
	return jsStringify(aud)
}

func axiomChosenOrg(token string) string {
	v, _ := axiomJWTClaims(token).Get("axiomDefaultOrg")
	s, _ := v.(string)
	return s
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
		d := override
		if d == "" {
			edge := ""
			if o.DefaultEdgeDeployment != nil {
				edge = *o.DefaultEdgeDeployment
			} else if o.Region != nil {
				edge = *o.Region
			}
			d = domain.AxiomDomains[0]
			if strings.Contains(edge, "eu-") {
				d = domain.AxiomDomains[1]
			}
		}
		orgs[i] = domain.AxiomOrg{ID: o.ID, Name: o.Name, MaxDatasets: o.MaxDatasets, Domain: d}
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
	datasets := [][2]string{{domain.DatasetLogs, "Keel container logs"}, {domain.DatasetTraces, "Keel OpenTelemetry traces"}}
	var missing [][2]string
	for _, d := range datasets {
		if !have[d[0]] {
			missing = append(missing, d)
		}
	}
	for _, d := range missing {
		name, description := d[0], d[1]
		if err := a.Axiom.CreateDataset(ctx, t, org.ID, name, description); err != nil {
			return domain.LogSink{}, errors.New(datasetCapMessage(org, own, missing, have, name, err))
		}
		have[name] = true
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

var axiom400RE = regexp.MustCompile(`^Axiom 400\b`)

func datasetCapMessage(org domain.AxiomOrg, own []string, missing [][2]string, have map[string]bool, name string, err error) string {
	var left []string
	for _, d := range missing {
		if !have[d[0]] {
			left = append(left, d[0])
		}
	}
	msg := err.Error()
	if org.MaxDatasets != nil && axiom400RE.MatchString(msg) && float64(len(own)) >= *org.MaxDatasets {
		limit := *org.MaxDatasets
		return org.Name + " is at its Axiom plan's limit of " + jsNumberString(limit) + " datasets (" +
			strings.Join(own, ", ") + "). Keel needs " + strings.Join(left, " and ") + ": delete " +
			jsNumberString(float64(len(own)+len(left))-limit) + " in Axiom or pick another org. (" + msg + ")"
	}
	return "Creating " + name + ": " + msg
}
