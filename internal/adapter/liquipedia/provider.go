package liquipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"cs2predictor/internal/domain/enrichment"
)

// Provider implements enrichment.TournamentMetadataProvider against the
// LPDB v3 /tournament endpoint.
type Provider struct {
	config Config
	client *http.Client
}

func NewProvider(config Config, client *http.Client) *Provider {
	return &Provider{config: config, client: client}
}

var _ enrichment.TournamentMetadataProvider = (*Provider)(nil)

type tournamentRecord struct {
	Name       string `json:"name"`
	ShortName  string `json:"shortname"`
	TickerName string `json:"tickername"`
}

type lpdbEnvelope struct {
	Result []tournamentRecord `json:"result"`
	Error  json.RawMessage    `json:"error"`
}

// EnrichTournament looks up a tournament by exact name (LPDB condition
// syntax: [[name::externalName]]) — case-sensitive and requires an exact
// match against Liquipedia's own tournament page name, which frequently
// differs from PandaScore's own event name. This is a known limitation: a
// miss here just means no enrichment for that event, never wrong data.
func (p *Provider) EnrichTournament(ctx context.Context, externalName string) (*enrichment.TournamentMetadata, error) {
	reqURL := p.config.BaseURL + "tournament?" + url.Values{
		"wiki":       {p.config.Wiki},
		"conditions": {fmt.Sprintf("[[name::%s]]", externalName)},
		"limit":      {"1"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Apikey "+p.config.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("liquipedia: invalid or missing API key")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("liquipedia: unexpected status %d", resp.StatusCode)
	}

	var envelope lpdbEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("liquipedia: decode response: %w", err)
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" && string(envelope.Error) != "[]" {
		return nil, fmt.Errorf("liquipedia: api error: %s", envelope.Error)
	}
	if len(envelope.Result) == 0 {
		return nil, nil
	}

	t := envelope.Result[0]
	meta := enrichment.TournamentMetadata{
		FullName: t.Name,
		Series:   firstNonEmpty(t.ShortName, t.TickerName),
	}
	if meta.FullName == "" && meta.Series == "" {
		return nil, nil
	}
	return &meta, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
