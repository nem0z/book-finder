package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// postJSON sends in as JSON to url with the given headers and decodes a 200 response into out.
func postJSON(ctx context.Context, client *http.Client, url string, header http.Header, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	for k, v := range header {
		req.Header[k] = v
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("re-ranker at %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("%w: %s: %s", ErrBadResponse, resp.Status, strings.TrimSpace(string(msg)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: %w", ErrBadResponse, err)
	}

	return nil
}
