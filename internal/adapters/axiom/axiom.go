// Package axiom implements app.Axiom: Axiom's REST API (APL queries, datasets, tokens, orgs),
// its OAuth server (Dynamic Client Registration, authorization code + PKCE) and the OTLP traces
// endpoint the relay forwards to. Plain net/http; the app builds every message from the errors
// returned here (*app.AxiomError, *app.OAuthError). See docs/go/spec/observability.md §3.2, §4.7.
package axiom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/app"
)

// Client talks to Axiom. The zero value is not usable; use New.
type Client struct {
	// HTTP is used for every call; tests point it at httptest servers.
	HTTP *http.Client
	// Timeout bounds REST and OAuth calls (the TS had none; Convex's action limit stood in).
	// The OTLP forward is bounded by the caller's context instead.
	Timeout time.Duration
}

var _ app.Axiom = (*Client)(nil)

// New is a client with a 60 s timeout per REST call.
func New() *Client {
	return &Client{HTTP: &http.Client{}, Timeout: 60 * time.Second}
}

func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.Timeout > 0 {
		return context.WithTimeout(ctx, c.Timeout)
	}
	return context.WithCancel(ctx)
}

// call is the TS call()/personal(): bearer + JSON headers (+ x-axiom-org-id), non-2xx →
// *app.AxiomError. Returns the response body.
func (c *Client) call(ctx context.Context, t app.AxiomTarget, orgID, method, path string, body any) ([]byte, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, app.AxiomBaseURL(t.Domain)+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	req.Header.Set("Content-Type", "application/json")
	if orgID != "" {
		req.Header.Set("X-Axiom-Org-Id", orgID)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, readErr := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &app.AxiomError{Status: res.StatusCode, Detail: app.CompactDetail(string(data))}
	}
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}

type aplBody struct {
	APL       string `json:"apl"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

// Query runs APL and returns the first table's rows (column-major → objects keyed by field).
func (c *Client) Query(ctx context.Context, t app.AxiomTarget, q app.AxiomQuery) ([]*app.JSONObject, error) {
	data, err := c.call(ctx, t, "", http.MethodPost, "/v1/datasets/_apl?format=tabular",
		aplBody{APL: q.APL, StartTime: q.StartTime, EndTime: q.EndTime})
	if err != nil {
		return nil, err
	}
	return tabularRows(data)
}

// tabularRows: {tables: [{fields: [{name}], columns: [[...], ...]}]} → rows of the first table.
// No table → no rows.
func tabularRows(data []byte) ([]*app.JSONObject, error) {
	v, err := app.DecodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("Axiom query: %w", err)
	}
	doc, _ := v.(*app.JSONObject)
	tables, _ := field(doc, "tables").([]any)
	if len(tables) == 0 {
		return []*app.JSONObject{}, nil
	}
	table, _ := tables[0].(*app.JSONObject)
	fields, _ := field(table, "fields").([]any)
	columns, _ := field(table, "columns").([]any)
	count := 0
	if len(columns) > 0 {
		first, _ := columns[0].([]any)
		count = len(first)
	}
	rows := make([]*app.JSONObject, 0, count)
	for i := 0; i < count; i++ {
		row := app.NewJSONObject()
		for c, f := range fields {
			name := ""
			if fo, ok := f.(*app.JSONObject); ok {
				if n, ok := field(fo, "name").(string); ok {
					name = n
				} else {
					name = jsKey(field(fo, "name"))
				}
			}
			var val any
			if c < len(columns) {
				if col, ok := columns[c].([]any); ok && i < len(col) {
					val = col[i]
				}
			}
			row.Set(name, val)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func field(o *app.JSONObject, k string) any {
	v, _ := o.Get(k)
	return v
}

// jsKey: a non-string field name used as a JS property key.
func jsKey(v any) string {
	if v == nil {
		return "undefined"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

type datasetBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (c *Client) CreateDataset(ctx context.Context, t app.AxiomTarget, orgID, name, description string) error {
	_, err := c.call(ctx, t, orgID, http.MethodPost, "/v2/datasets", datasetBody{Name: name, Description: description})
	return err
}

func (c *Client) Datasets(ctx context.Context, t app.AxiomTarget, orgID string) ([]app.AxiomDataset, error) {
	data, err := c.call(ctx, t, orgID, http.MethodGet, "/v2/datasets", nil)
	if err != nil {
		return nil, err
	}
	var list []struct {
		Name        string `json:"name"`
		SharedByOrg any    `json:"sharedByOrg"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("Axiom datasets: %w", err)
	}
	out := make([]app.AxiomDataset, len(list))
	for i, d := range list {
		out[i] = app.AxiomDataset{Name: d.Name, Shared: truthy(d.SharedByOrg)}
	}
	return out, nil
}

// truthy is JS truthiness of a JSON value.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

type capability struct {
	Ingest []string `json:"ingest"`
	Query  []string `json:"query"`
}

type tokenBody struct {
	Name                string                `json:"name"`
	Description         string                `json:"description"`
	DatasetCapabilities map[string]capability `json:"datasetCapabilities"`
	OrgCapabilities     struct{}              `json:"orgCapabilities"`
}

func (c *Client) MintToken(ctx context.Context, t app.AxiomTarget, orgID string, r app.AxiomTokenRequest) (string, error) {
	body := tokenBody{Name: r.Name, Description: r.Description, DatasetCapabilities: map[string]capability{}}
	for _, d := range r.Datasets {
		body.DatasetCapabilities[d] = capability{Ingest: []string{"create"}, Query: []string{"read"}}
	}
	data, err := c.call(ctx, t, orgID, http.MethodPost, "/v2/tokens", body)
	if err != nil {
		return "", err
	}
	var minted struct {
		Token any `json:"token"`
	}
	if err := json.Unmarshal(data, &minted); err != nil {
		return "", fmt.Errorf("Axiom token: %w", err)
	}
	if s, ok := minted.Token.(string); ok {
		return s, nil
	}
	if truthy(minted.Token) {
		return fmt.Sprint(minted.Token), nil
	}
	return "", nil
}

func (c *Client) Orgs(ctx context.Context, t app.AxiomTarget) ([]app.AxiomOrgInfo, error) {
	data, err := c.call(ctx, t, "", http.MethodGet, "/v2/orgs", nil)
	if err != nil {
		return nil, err
	}
	var list []struct {
		ID                    string  `json:"id"`
		Name                  string  `json:"name"`
		DefaultEdgeDeployment *string `json:"defaultEdgeDeployment"`
		Region                *string `json:"region"`
		License               *struct {
			MaxDatasets *float64 `json:"maxDatasets"`
		} `json:"license"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("Axiom orgs: %w", err)
	}
	out := make([]app.AxiomOrgInfo, len(list))
	for i, o := range list {
		out[i] = app.AxiomOrgInfo{ID: o.ID, Name: o.Name, DefaultEdgeDeployment: o.DefaultEdgeDeployment, Region: o.Region}
		if o.License != nil {
			out[i].MaxDatasets = o.License.MaxDatasets
		}
	}
	return out, nil
}

// oauthPost posts to the OAuth server and decodes the JSON answer (anything else → nil body).
func (c *Client) oauthPost(ctx context.Context, endpoint, contentType string, body []byte) (int, *app.JSONObject, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	v, err := app.DecodeJSON(data)
	if err != nil {
		return res.StatusCode, nil, nil
	}
	obj, _ := v.(*app.JSONObject)
	return res.StatusCode, obj, nil
}

func stringField(o *app.JSONObject, k string) string {
	s, _ := field(o, k).(string)
	return s
}

func (c *Client) RegisterClient(ctx context.Context, authURL, redirectURI string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"client_name":                "Keel",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", err
	}
	status, obj, err := c.oauthPost(ctx, authURL+"/oauth2/register", "application/json", body)
	if err != nil {
		return "", err
	}
	id := stringField(obj, "client_id")
	if status < 200 || status > 299 || id == "" {
		return "", &app.OAuthError{Status: status, Body: obj}
	}
	return id, nil
}

func (c *Client) ExchangeCode(ctx context.Context, authURL string, x app.AxiomCodeExchange) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", x.Code)
	form.Set("code_verifier", x.Verifier)
	form.Set("redirect_uri", x.RedirectURI)
	form.Set("client_id", x.ClientID)
	status, obj, err := c.oauthPost(ctx, authURL+"/oauth2/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return "", err
	}
	token := stringField(obj, "access_token")
	if status < 200 || status > 299 || token == "" {
		return "", &app.OAuthError{Status: status, Body: obj}
	}
	return token, nil
}

// ForwardTraces posts the OTLP body unchanged to <base>/v1/traces.
func (c *Client) ForwardTraces(ctx context.Context, f app.OTLPForward) (app.HTTPReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.AxiomBaseURL(f.Domain)+"/v1/traces", bytes.NewReader(f.Body))
	if err != nil {
		return app.HTTPReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+f.Token)
	req.Header.Set("X-Axiom-Dataset", f.Dataset)
	req.Header.Set("Content-Type", f.ContentType)
	if f.ContentEncoding != "" {
		req.Header.Set("Content-Encoding", f.ContentEncoding)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return app.HTTPReply{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil && !errors.Is(err, io.EOF) {
		return app.HTTPReply{}, err
	}
	return app.HTTPReply{Status: res.StatusCode, ContentType: strings.TrimSpace(res.Header.Get("Content-Type")), Body: data}, nil
}
