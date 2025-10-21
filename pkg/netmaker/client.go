package netmaker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client is the interface for Netmaker API operations
// This allows easy mocking in tests
// The client works with ALL networks - network is passed as parameter where needed
type Client interface {
	// Authenticate obtains a JWT token from Netmaker API
	Authenticate(ctx context.Context) error

	// ListHosts returns all hosts in Netmaker (global, not per-network)
	ListHosts(ctx context.Context) ([]Host, error)

	// ListNodes returns all nodes across all networks
	ListNodes(ctx context.Context) ([]Node, error)

	// ListEgress returns all egress gateways for the specified network
	ListEgress(ctx context.Context, network string) ([]Egress, error)

	// CreateEgress creates a new egress gateway (network specified in req.Network)
	CreateEgress(ctx context.Context, req EgressReq) (*Egress, error)

	// UpdateEgress updates an existing egress gateway (network specified in req.Network)
	UpdateEgress(ctx context.Context, req EgressReq) (*Egress, error)

	// DeleteEgress removes an egress gateway by ID
	DeleteEgress(ctx context.Context, egressID string) error
}

// HTTPClient implements Client using Netmaker REST API
// Works with all networks - network is passed as parameter to methods that need it
type HTTPClient struct {
	baseURL  string
	username string
	password string
	client   *http.Client

	// Token management (internal state)
	tokenMu sync.RWMutex
	token   string
}

// NewHTTPClient creates a new Netmaker HTTP client for all networks
// Returns error for validation failures, never panics
func NewHTTPClient(baseURL, username, password string) (*HTTPClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL is required")
	}
	if username == "" {
		return nil, fmt.Errorf("username is required")
	}
	if password == "" {
		return nil, fmt.Errorf("password is required")
	}

	return &HTTPClient{
		baseURL:  baseURL,
		username: username,
		password: password,
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Authenticate obtains a JWT token from Netmaker API
func (c *HTTPClient) Authenticate(ctx context.Context) error {
	authURL := fmt.Sprintf("%s/api/users/adm/authenticate", c.baseURL)

	payload := AuthRequest{
		Username: c.username,
		Password: c.password,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal auth payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("authentication request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authentication failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Validate Content-Type is JSON
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
	}

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return fmt.Errorf("failed to decode auth response: %w", err)
	}

	// Check JSON Code field if present
	if authResp.Code != 0 && authResp.Code != http.StatusOK {
		return fmt.Errorf("authentication failed with API code %d: %s", authResp.Code, authResp.Message)
	}

	// Validate we got a token
	if authResp.Response.AuthToken == "" {
		return fmt.Errorf("authentication succeeded but no token in response")
	}

	c.tokenMu.Lock()
	c.token = authResp.Response.AuthToken
	c.tokenMu.Unlock()

	return nil
}

// getToken returns the current token, authenticating if needed
func (c *HTTPClient) getToken(ctx context.Context) (string, error) {
	c.tokenMu.RLock()
	token := c.token
	c.tokenMu.RUnlock()

	if token == "" {
		// No token yet - authenticate
		if err := c.Authenticate(ctx); err != nil {
			return "", err
		}
		c.tokenMu.RLock()
		token = c.token
		c.tokenMu.RUnlock()
	}

	return token, nil
}

// doRequest performs an HTTP request with automatic token management
// Handles authentication, 401 retry, and error response parsing
func (c *HTTPClient) doRequest(ctx context.Context, method, url string, body interface{}) (*http.Response, error) {
	// Get current token (authenticates if needed)
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get auth token: %w", err)
	}

	// Marshal request body if provided
	var reqBody io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(bodyBytes)
	}

	// Build request
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Content-Type", "application/json")

	// Execute request
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Handle 401 - token expired, re-authenticate and retry once
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()

		if err := c.Authenticate(ctx); err != nil {
			return nil, fmt.Errorf("re-authentication failed: %w", err)
		}

		// Retry request with new token
		token, _ = c.getToken(ctx)

		// Rebuild request body
		if body != nil {
			bodyBytes, _ := json.Marshal(body)
			reqBody = bytes.NewReader(bodyBytes)
		}

		req, err = http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("failed to create retry request: %w", err)
		}
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
		req.Header.Set("Content-Type", "application/json")

		resp, err = c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("retry request failed: %w", err)
		}
	}

	return resp, nil
}

// ListHosts implements Client interface
func (c *HTTPClient) ListHosts(ctx context.Context) ([]Host, error) {
	url := fmt.Sprintf("%s/api/hosts", c.baseURL)

	resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ListHosts failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Validate Content-Type is JSON
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
	}

	var hosts []Host
	if err := json.NewDecoder(resp.Body).Decode(&hosts); err != nil {
		return nil, fmt.Errorf("failed to decode hosts list: %w", err)
	}

	return hosts, nil
}

// ListNodes implements Client interface - returns nodes from all networks
func (c *HTTPClient) ListNodes(ctx context.Context) ([]Node, error) {
	url := fmt.Sprintf("%s/api/nodes", c.baseURL)

	resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ListNodes failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Validate Content-Type is JSON
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
	}

	var nodes []Node
	if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
		return nil, fmt.Errorf("failed to decode nodes list: %w", err)
	}

	return nodes, nil
}

// ListEgress implements Client interface
func (c *HTTPClient) ListEgress(ctx context.Context, network string) ([]Egress, error) {
	url := fmt.Sprintf("%s/api/v1/egress?network=%s", c.baseURL, network)

	resp, err := c.doRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ListEgress failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Validate Content-Type is JSON
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
	}

	var egressResp EgressResponse
	if err := json.NewDecoder(resp.Body).Decode(&egressResp); err != nil {
		return nil, fmt.Errorf("failed to decode egress list: %w", err)
	}

	// Check JSON Code field if present
	if egressResp.Code != 0 && egressResp.Code != http.StatusOK {
		return nil, fmt.Errorf("ListEgress failed with API code %d: %s", egressResp.Code, egressResp.Message)
	}

	return egressResp.Response, nil
}

// CreateEgress implements Client interface
func (c *HTTPClient) CreateEgress(ctx context.Context, req EgressReq) (*Egress, error) {
	url := fmt.Sprintf("%s/api/v1/egress", c.baseURL)

	resp, err := c.doRequest(ctx, http.MethodPost, url, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("CreateEgress failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Validate Content-Type is JSON
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
	}

	var createResp EgressCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		return nil, fmt.Errorf("failed to decode egress response: %w", err)
	}

	// Check JSON Code field if present
	if createResp.Code != 0 && createResp.Code != http.StatusOK && createResp.Code != http.StatusCreated {
		return nil, fmt.Errorf("CreateEgress failed with API code %d: %s", createResp.Code, createResp.Message)
	}

	return &createResp.Response, nil
}

// UpdateEgress implements Client interface
func (c *HTTPClient) UpdateEgress(ctx context.Context, req EgressReq) (*Egress, error) {
	url := fmt.Sprintf("%s/api/v1/egress", c.baseURL)

	resp, err := c.doRequest(ctx, http.MethodPut, url, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Check HTTP status first
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("UpdateEgress failed with HTTP status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Validate Content-Type is JSON
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("expected JSON response, got Content-Type: %s", contentType)
	}

	var updateResp EgressUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&updateResp); err != nil {
		return nil, fmt.Errorf("failed to decode egress response: %w", err)
	}

	// Check JSON Code field if present
	if updateResp.Code != 0 && updateResp.Code != http.StatusOK {
		return nil, fmt.Errorf("UpdateEgress failed with API code %d: %s", updateResp.Code, updateResp.Message)
	}

	return &updateResp.Response, nil
}

// DeleteEgress implements Client interface
func (c *HTTPClient) DeleteEgress(ctx context.Context, egressID string) error {
	url := fmt.Sprintf("%s/api/v1/egress?id=%s", c.baseURL, egressID)

	resp, err := c.doRequest(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DeleteEgress failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
