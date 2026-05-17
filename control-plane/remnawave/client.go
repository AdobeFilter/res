// Package remnawave is a thin HTTP client for the Remnawave v2.7 panel API.
// Only the endpoints we need (create user, get user) are implemented; other
// admin features are managed by operators directly in the panel UI.
package remnawave

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FreeTierBytes is the monthly quota handed to every account on register.
const FreeTierBytes int64 = 1 * 128 * 1024 * 1024

// Client talks to a Remnawave instance over HTTPS with a bearer JWT.
// Construct via NewClient; methods are safe for concurrent use.
type Client struct {
	baseURL    string // e.g. https://panel.example.com
	token      string // JWT issued in the panel's API Tokens page
	squadUUID  string // default internal-squad to attach new users to
	httpClient *http.Client
}

func NewClient(baseURL, token, squadUUID string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		squadUUID:  squadUUID,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether the client has enough config to talk to a panel.
// When false, the control-plane must skip Remnawave-coupled flows.
func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.token != "" && c.squadUUID != ""
}

// User is the subset of Remnawave's user object the control-plane cares about.
// Extra fields in the JSON response are ignored.
type User struct {
	UUID            string `json:"uuid"`
	ShortUUID       string `json:"shortUuid"`
	Username        string `json:"username"`
	Email           string `json:"email"`
	Status          string `json:"status"`
	TrafficLimit    int64  `json:"trafficLimitBytes"`
	TrafficStrategy string `json:"trafficLimitStrategy"`
	SubscriptionURL string `json:"subscriptionUrl"`
	UserTraffic     struct {
		UsedTrafficBytes int64 `json:"usedTrafficBytes"`
	} `json:"userTraffic"`
}

// CreateUser provisions a Remnawave user with a monthly traffic budget.
// username must be alphanumeric / underscore — we pass the account UUID
// stripped of dashes so it's globally unique and panel-safe.
// Returns the created User on success.
func (c *Client) CreateUser(username, email string, trafficLimitBytes int64) (*User, error) {
	body := map[string]interface{}{
		"username":             username,
		"email":                email,
		"status":               "ACTIVE",
		"trafficLimitBytes":    trafficLimitBytes,
		"trafficLimitStrategy": "MONTH",
		"expireAt":             time.Now().AddDate(50, 0, 0).Format(time.RFC3339),
		"activeInternalSquads": []string{c.squadUUID},
	}
	var out struct {
		Response User `json:"response"`
	}
	if err := c.do("POST", "/api/users", body, &out); err != nil {
		return nil, err
	}
	return &out.Response, nil
}

// GetUser fetches the current state (including userTraffic.usedTrafficBytes)
// of a previously-created user.
func (c *Client) GetUser(uuid string) (*User, error) {
	var out struct {
		Response User `json:"response"`
	}
	if err := c.do("GET", "/api/users/"+uuid, nil, &out); err != nil {
		return nil, err
	}
	return &out.Response, nil
}

func (c *Client) do(method, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("remnawave %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
	}
	return nil
}
