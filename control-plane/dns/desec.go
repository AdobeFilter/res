package dns

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const desecAPIBase = "https://desec.io/api/v1"

// DNSClient manages automatic DNS record creation for exit nodes.
type DNSClient struct {
	apiToken   string
	domain     string // e.g. "valhalla.dedyn.io"
	httpClient *http.Client
}

func NewDNSClient(apiToken, domain string) *DNSClient {
	return &DNSClient{
		apiToken:   apiToken,
		domain:     domain,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled returns whether DNS auto-domain is configured.
func (c *DNSClient) Enabled() bool {
	return c.apiToken != "" && c.domain != ""
}

// CreateExitNodeDomain creates a DNS A record: <random>.<domain> → ip.
// Returns the full domain name (e.g., "ex-a1b2c3d4.valhalla.dedyn.io").
func (c *DNSClient) CreateExitNodeDomain(ip string) (string, error) {
	subdomain, err := randomSubdomain()
	if err != nil {
		return "", fmt.Errorf("generate subdomain: %w", err)
	}

	fqdn := subdomain + "." + c.domain

	// deSEC API: POST /api/v1/domains/{domain}/rrsets/
	body := map[string]interface{}{
		"subname": subdomain,
		"type":    "A",
		"records": []string{ip},
		"ttl":     3600,
	}

	jsonBody, _ := json.Marshal(body)

	url := fmt.Sprintf("%s/domains/%s/rrsets/", desecAPIBase, c.domain)
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Token "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("desec request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// 201 Created = success, 409 Conflict = already exists
	if resp.StatusCode == http.StatusCreated {
		return fqdn, nil
	}

	if resp.StatusCode == http.StatusConflict {
		// Record already exists — update it with PUT/PATCH
		return fqdn, c.updateRecord(subdomain, ip)
	}

	return "", fmt.Errorf("desec API %d: %s", resp.StatusCode, string(respBody))
}

// updateRecord updates an existing A record via PATCH.
func (c *DNSClient) updateRecord(subdomain, ip string) error {
	body := map[string]interface{}{
		"records": []string{ip},
		"ttl":     3600,
	}
	jsonBody, _ := json.Marshal(body)

	url := fmt.Sprintf("%s/domains/%s/rrsets/%s/A/", desecAPIBase, c.domain, subdomain)
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("desec patch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("desec patch %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func randomSubdomain() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ex-" + hex.EncodeToString(b), nil
}
