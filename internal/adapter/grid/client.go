package grid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// graphQLError is one entry of a GraphQL response's top-level "errors"
// array — the standard GraphQL error envelope, independent of HTTP status
// (GRID, like most GraphQL APIs, can return 200 with errors present).
type graphQLError struct {
	Message string `json:"message"`
}

type graphQLEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphQLError  `json:"errors"`
}

// execute POSTs a GraphQL query to url, authenticated with apiKey via the
// x-api-key header (confirmed against GRID's own example client — GRID's
// public docs mention x-auth-key in prose, but real working code uses
// x-api-key; the header actually sent here is the latter), and decodes the
// "data" field of the response into out.
func execute(ctx context.Context, client *http.Client, url, apiKey, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("grid: unexpected status %d", resp.StatusCode)
	}

	var envelope graphQLEnvelope
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("grid: decode response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("grid: graphql error: %s", envelope.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}
