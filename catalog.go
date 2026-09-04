package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const maxModelCatalogResponseBytes = 8 << 20

// ModelCatalogEntry describes one picker-visible model available to the
// authenticated ChatGPT account.
type ModelCatalogEntry struct {
	Slug                      string
	DisplayName               string
	Description               *string
	Priority                  int
	DefaultReasoningEffort    *string
	SupportedReasoningEfforts []ReasoningEffortOption
}

// ReasoningEffortOption describes one server-supported reasoning setting.
// Effort is intentionally an open string so new server values remain usable.
type ReasoningEffortOption struct {
	Effort      string
	Description string
}

// ModelCatalogHTTPError reports a non-success response from the model catalog.
// It intentionally exposes only the status code, never the response body.
type ModelCatalogHTTPError struct {
	StatusCode int
}

func (e *ModelCatalogHTTPError) Error() string {
	return fmt.Sprintf("codexauth: list models: HTTP status %d", e.StatusCode)
}

// ListModels fetches the current picker-visible model catalog for this
// client's authenticated account. clientVersion is an opaque, non-secret
// compatibility value supplied by the caller and is passed through unchanged.
// The result is stable-sorted by ascending server priority; reasoning options
// retain their server order. The package does not cache results.
//
// Calls through one Client are serialized so stale rotating refresh tokens are
// not used concurrently. Distinct clients and processes are not coalesced.
// Cancellation during a stale-token refresh may return only after the existing
// detached refresh persistence step finishes, but no catalog request is sent
// afterward.
func (c *Client) ListModels(ctx context.Context, clientVersion string) ([]ModelCatalogEntry, error) {
	if strings.TrimSpace(clientVersion) == "" {
		return nil, errors.New("codexauth: list models: clientVersion must not be empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("codexauth: list models: %w", err)
	}

	release, err := c.acquireCatalogGate(ctx)
	if err != nil {
		return nil, fmt.Errorf("codexauth: list models: %w", err)
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("codexauth: list models: %w", err)
	}

	endpoint, err := modelsEndpoint(c.endpoint)
	if err != nil {
		return nil, fmt.Errorf("codexauth: list models: %w", err)
	}
	httpClient, err := httpClientForApp(c.appName, endpoint, c.logger, c.load, c.save, c.delete)
	if err != nil {
		return nil, fmt.Errorf("codexauth: list models: %w", err)
	}
	transport, ok := httpClient.Transport.(*codexTransport)
	if !ok {
		return nil, errors.New("codexauth: list models: internal transport unavailable")
	}
	transport.safeRefreshErrors = true

	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("codexauth: list models: build request URL")
	}
	query := requestURL.Query()
	query.Set("client_version", clientVersion)
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("codexauth: list models: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("codexauth: list models: %w", ctxErr)
	}
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("codexauth: list models: request: %w", catalogRequestCause(err))
	}
	return decodeModelCatalogResponse(resp)
}

// catalogRequestCause removes net/http's *url.Error wrapper because its Error
// string contains the full request URL, including the caller-supplied
// client_version value. The underlying transport error retains error identity
// without retaining that value.
func catalogRequestCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

type modelCatalogWireResponse struct {
	Models *[]modelCatalogWireEntry `json:"models"`
}

type modelCatalogWireEntry struct {
	Slug                      *string                            `json:"slug"`
	DisplayName               *string                            `json:"display_name"`
	Description               *string                            `json:"description"`
	DefaultReasoningEffort    *string                            `json:"default_reasoning_level"`
	SupportedReasoningEfforts *[]modelCatalogWireReasoningOption `json:"supported_reasoning_levels"`
	Visibility                *string                            `json:"visibility"`
	Priority                  *int                               `json:"priority"`
}

type modelCatalogWireReasoningOption struct {
	Effort      *string `json:"effort"`
	Description *string `json:"description"`
}

func decodeModelCatalogResponse(resp *http.Response) ([]ModelCatalogEntry, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("codexauth: list models: empty HTTP response")
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &ModelCatalogHTTPError{StatusCode: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelCatalogResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("codexauth: list models: read response: %w", err)
	}
	if len(body) > maxModelCatalogResponseBytes {
		return nil, errors.New("codexauth: list models: response exceeds size limit")
	}

	var wire modelCatalogWireResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("codexauth: list models: decode response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("codexauth: list models: trailing JSON data")
	}
	if wire.Models == nil {
		return nil, errors.New("codexauth: list models: missing models")
	}

	result := make([]ModelCatalogEntry, 0, len(*wire.Models))
	for i := range *wire.Models {
		entry, visible, err := convertModelCatalogEntry(&(*wire.Models)[i])
		if err != nil {
			return nil, fmt.Errorf("codexauth: list models: model %d: %w", i, err)
		}
		if visible {
			result = append(result, entry)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Priority < result[j].Priority })
	return result, nil
}

func convertModelCatalogEntry(wire *modelCatalogWireEntry) (ModelCatalogEntry, bool, error) {
	if wire.Slug == nil || *wire.Slug == "" {
		return ModelCatalogEntry{}, false, errors.New("missing slug")
	}
	if wire.DisplayName == nil || *wire.DisplayName == "" {
		return ModelCatalogEntry{}, false, errors.New("missing display_name")
	}
	if wire.Visibility == nil || *wire.Visibility == "" {
		return ModelCatalogEntry{}, false, errors.New("missing visibility")
	}
	if wire.Priority == nil {
		return ModelCatalogEntry{}, false, errors.New("missing priority")
	}
	if wire.SupportedReasoningEfforts == nil {
		return ModelCatalogEntry{}, false, errors.New("missing supported_reasoning_levels")
	}
	if wire.DefaultReasoningEffort != nil && *wire.DefaultReasoningEffort == "" {
		return ModelCatalogEntry{}, false, errors.New("empty default_reasoning_level")
	}

	options := make([]ReasoningEffortOption, len(*wire.SupportedReasoningEfforts))
	for i, option := range *wire.SupportedReasoningEfforts {
		if option.Effort == nil || *option.Effort == "" {
			return ModelCatalogEntry{}, false, fmt.Errorf("reasoning option %d missing effort", i)
		}
		if option.Description == nil {
			return ModelCatalogEntry{}, false, fmt.Errorf("reasoning option %d missing description", i)
		}
		options[i] = ReasoningEffortOption{Effort: *option.Effort, Description: *option.Description}
	}

	entry := ModelCatalogEntry{
		Slug:                      *wire.Slug,
		DisplayName:               *wire.DisplayName,
		Description:               cloneStringPointer(wire.Description),
		Priority:                  *wire.Priority,
		DefaultReasoningEffort:    cloneStringPointer(wire.DefaultReasoningEffort),
		SupportedReasoningEfforts: options,
	}
	return entry, *wire.Visibility == "list", nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
