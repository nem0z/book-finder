package understand

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// cognitiveServicesResource is the token audience of Azure OpenAI.
	cognitiveServicesResource = "https://cognitiveservices.azure.com"
	identityAPIVersion        = "2019-08-01"

	// tokenRefreshMargin renews a token this long before it expires.
	tokenRefreshMargin = 5 * time.Minute
)

// managedIdentity gets Microsoft Entra ID tokens for Azure OpenAI from the managed identity endpoint of
// Azure Container Apps (https://learn.microsoft.com/azure/container-apps/managed-identity#rest-endpoint-reference),
// and caches them until shortly before they expire.
type managedIdentity struct {
	endpoint string
	secret   string
	clientID string // of a user-assigned identity; empty for the system-assigned one
	client   *http.Client
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

type identityToken struct {
	AccessToken string `json:"access_token"`
	// ExpiresOn is in Unix seconds, sent as a string.
	ExpiresOn json.Number `json:"expires_on"`
}

// managedIdentityFromEnv returns the identity of the container app it runs in: IDENTITY_ENDPOINT and
// IDENTITY_HEADER are set by Container Apps, AZURE_CLIENT_ID picks a user-assigned identity. nil outside
// Container Apps.
func managedIdentityFromEnv() *managedIdentity {
	endpoint, secret := os.Getenv("IDENTITY_ENDPOINT"), os.Getenv("IDENTITY_HEADER")
	if endpoint == "" || secret == "" {
		return nil
	}

	return &managedIdentity{
		endpoint: endpoint,
		secret:   secret,
		clientID: os.Getenv("AZURE_CLIENT_ID"),
		client:   &http.Client{Timeout: requestTimeout},
		now:      time.Now,
	}
}

// Token returns a bearer token for Azure OpenAI.
func (m *managedIdentity) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.token != "" && m.now().Before(m.expires.Add(-tokenRefreshMargin)) {
		return m.token, nil
	}

	q := url.Values{"resource": {cognitiveServicesResource}, "api-version": {identityAPIVersion}}
	if m.clientID != "" {
		q.Set("client_id", m.clientID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("X-IDENTITY-HEADER", m.secret)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("managed identity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return "", fmt.Errorf("managed identity: %w: %s: %s", ErrBadResponse, resp.Status, strings.TrimSpace(string(msg)))
	}

	var t identityToken
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("managed identity: %w: %w", ErrBadResponse, err)
	}

	expires, err := strconv.ParseInt(t.ExpiresOn.String(), 10, 64)
	if err != nil || t.AccessToken == "" {
		return "", fmt.Errorf("managed identity: %w: no token or expiry", ErrBadResponse)
	}

	m.token, m.expires = t.AccessToken, time.Unix(expires, 0)

	return m.token, nil
}
