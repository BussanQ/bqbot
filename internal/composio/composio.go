// Package composio connects applications while keeping the API key in the OS credential store.
package composio

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

	"github.com/egoist/mygo"
)

const secretKey = "composio-api-key"

// Secrets is implemented by mygo's system keychain and by isolated test stores.
type Secrets interface {
	Get(string) ([]byte, error)
	Set(string, []byte) error
	Delete(string) error
}

// Client never serializes credentials or provider error bodies.
type Client struct {
	Secrets Secrets
	BaseURL string
	HTTP    *http.Client
}

func New() *Client {
	return &Client{Secrets: mygo.Secrets.Service("bqbot"), BaseURL: "https://backend.composio.dev/api/v3", HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Configured() (bool, error) {
	key, err := c.Secrets.Get(secretKey)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return false, nil
	}
	return len(key) > 0, err
}
func (c *Client) SaveKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > mygo.MaxSecretSize || strings.ContainsAny(key, "\r\n\x00") {
		return errors.New("invalid Composio API key")
	}
	return c.Secrets.Set(secretKey, []byte(key))
}
func (c *Client) DeleteKey() error { return c.Secrets.Delete(secretKey) }

// Headers resolves the credential only at the moment an outbound request needs it.
func (c *Client) Headers() (map[string]string, error) {
	key, err := c.Secrets.Get(secretKey)
	if err != nil || len(key) == 0 {
		return nil, errors.New("Composio API key is unavailable in the system keychain")
	}
	return map[string]string{"x-api-key": string(key)}, nil
}

type Link struct {
	URL       string `json:"redirect_url"`
	AccountID string `json:"connected_account_id"`
}
type Account struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Toolkit struct {
		Slug string `json:"slug"`
	} `json:"toolkit"`
}

func (c *Client) Connect(ctx context.Context, userID, authConfigID string) (Link, error) {
	var link Link
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(authConfigID) == "" {
		return link, errors.New("user ID and auth config ID are required")
	}
	err := c.request(ctx, "POST", "/connected_accounts/link", map[string]string{"user_id": userID, "auth_config_id": authConfigID}, &link)
	if err == nil {
		u, e := url.Parse(link.URL)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return Link{}, errors.New("Composio returned an invalid authorization URL")
		}
	}
	return link, err
}
func (c *Client) Accounts(ctx context.Context, userID string) ([]Account, error) {
	var result struct {
		Items []Account `json:"items"`
	}
	err := c.request(ctx, "GET", "/connected_accounts?user_ids="+url.QueryEscape(userID), nil, &result)
	return result.Items, err
}
func (c *Client) request(ctx context.Context, method, path string, body, out any) error {
	headers, err := c.Headers()
	if err != nil {
		return err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid Composio API address")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Never forward the API key to a redirect target.
	hc := *c.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New("Composio request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Composio returned HTTP %d", resp.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
		return errors.New("invalid Composio response")
	}
	return nil
}
