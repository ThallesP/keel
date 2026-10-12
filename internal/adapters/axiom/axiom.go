package axiom

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: time.Minute}}
}

func (c *Client) call(ctx context.Context, t app.AxiomTarget, orgID, method, path string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, domain.AxiomBaseURL(t.Domain)+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Token)
	req.Header.Set("Content-Type", "application/json")
	if orgID != "" {
		req.Header.Set("X-Axiom-Org-Id", orgID)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, readErr := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, &app.AxiomError{Status: res.StatusCode, Detail: app.CompactText(string(data), 200)}
	}
	if readErr != nil {
		return nil, readErr
	}
	return data, nil
}

func aplTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

type aplBody struct {
	APL       string `json:"apl"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

func (c *Client) Query(ctx context.Context, t app.AxiomTarget, q app.AxiomQuery) ([]app.AxiomRow, error) {
	data, err := c.call(ctx, t, "", http.MethodPost, "/v1/datasets/_apl?format=tabular",
		aplBody{APL: q.APL, StartTime: aplTime(q.StartTime), EndTime: aplTime(q.EndTime)})
	if err != nil {
		return nil, err
	}
	return tabularRows(data)
}

type tabularResult struct {
	Tables []struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
		Columns [][]json.RawMessage `json:"columns"`
	} `json:"tables"`
}

func tabularRows(data []byte) ([]app.AxiomRow, error) {
	var result tabularResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("Axiom query: %w", err)
	}
	if len(result.Tables) == 0 || len(result.Tables[0].Columns) == 0 {
		return nil, nil
	}
	table := result.Tables[0]
	count := len(table.Columns[0])
	ragged := slices.ContainsFunc(table.Columns, func(column []json.RawMessage) bool { return len(column) != count })
	if len(table.Columns) != len(table.Fields) || ragged {
		return nil, errors.New("Axiom query: columns do not line up with fields")
	}
	rows := make([]app.AxiomRow, count)
	for i := range rows {
		rows[i] = make(app.AxiomRow, len(table.Fields))
		for c, field := range table.Fields {
			rows[i][field.Name] = table.Columns[c][i]
		}
	}
	return rows, nil
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
		SharedByOrg string `json:"sharedByOrg"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("Axiom datasets: %w", err)
	}
	out := make([]app.AxiomDataset, len(list))
	for i, d := range list {
		out[i] = app.AxiomDataset{Name: d.Name, Shared: d.SharedByOrg != ""}
	}
	return out, nil
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
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &minted); err != nil {
		return "", fmt.Errorf("Axiom token: %w", err)
	}
	return minted.Token, nil
}

func (c *Client) Orgs(ctx context.Context, t app.AxiomTarget) ([]app.AxiomOrgInfo, error) {
	data, err := c.call(ctx, t, "", http.MethodGet, "/v2/orgs", nil)
	if err != nil {
		return nil, err
	}
	var list []struct {
		ID                    string `json:"id"`
		Name                  string `json:"name"`
		DefaultEdgeDeployment string `json:"defaultEdgeDeployment"`
		Region                string `json:"region"`
		License               struct {
			MaxDatasets int `json:"maxDatasets"`
		} `json:"license"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("Axiom orgs: %w", err)
	}
	out := make([]app.AxiomOrgInfo, len(list))
	for i, o := range list {
		out[i] = app.AxiomOrgInfo{ID: o.ID, Name: o.Name, Edge: cmp.Or(o.DefaultEdgeDeployment, o.Region), MaxDatasets: o.License.MaxDatasets}
	}
	return out, nil
}

type oauthReply struct {
	ClientID         string `json:"client_id"`
	AccessToken      string `json:"access_token"`
	ErrorCode        string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (r oauthReply) failure(status int) *app.OAuthError {
	return &app.OAuthError{Status: status, ErrorCode: r.ErrorCode, ErrorDescription: r.ErrorDescription}
}

func (c *Client) oauthPost(ctx context.Context, endpoint, contentType string, body []byte) (int, oauthReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, oauthReply{}, err
	}
	req.Header.Set("Content-Type", contentType)
	res, err := c.http.Do(req)
	if err != nil {
		return 0, oauthReply{}, err
	}
	defer res.Body.Close()
	var reply oauthReply
	_ = json.NewDecoder(res.Body).Decode(&reply)
	return res.StatusCode, reply, nil
}

type registerBody struct {
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	RedirectURIs            []string `json:"redirect_uris"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (c *Client) RegisterClient(ctx context.Context, authURL, redirectURI string) (string, error) {
	body, err := json.Marshal(registerBody{
		ClientName:              "Keel",
		GrantTypes:              []string{"authorization_code"},
		RedirectURIs:            []string{redirectURI},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	})
	if err != nil {
		return "", err
	}
	status, reply, err := c.oauthPost(ctx, authURL+"/oauth2/register", "application/json", body)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 || reply.ClientID == "" {
		return "", reply.failure(status)
	}
	return reply.ClientID, nil
}

func (c *Client) ExchangeCode(ctx context.Context, authURL string, x app.AxiomCodeExchange) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {x.Code},
		"code_verifier": {x.Verifier},
		"redirect_uri":  {x.RedirectURI},
		"client_id":     {x.ClientID},
	}
	status, reply, err := c.oauthPost(ctx, authURL+"/oauth2/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 || reply.AccessToken == "" {
		return "", reply.failure(status)
	}
	return reply.AccessToken, nil
}

func (c *Client) ForwardTraces(ctx context.Context, f app.OTLPForward) (app.HTTPReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, domain.AxiomBaseURL(f.Domain)+"/v1/traces", bytes.NewReader(f.Body))
	if err != nil {
		return app.HTTPReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+f.Token)
	req.Header.Set("X-Axiom-Dataset", f.Dataset)
	req.Header.Set("Content-Type", f.ContentType)
	if f.ContentEncoding != "" {
		req.Header.Set("Content-Encoding", f.ContentEncoding)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return app.HTTPReply{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return app.HTTPReply{}, err
	}
	return app.HTTPReply{Status: res.StatusCode, ContentType: res.Header.Get("Content-Type"), Body: data}, nil
}
