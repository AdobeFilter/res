package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
	"valhalla/common/api"
	"valhalla/common/protocol"
)

// Client handles communication with the control plane.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *zap.Logger
}

func NewClient(baseURL, token string, logger *zap.Logger) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// RegisterNode registers this exit node with the control plane.
func (c *Client) RegisterNode(ctx context.Context, name, publicKey string) (*protocol.NodeRegisterResponse, error) {
	body, _ := json.Marshal(protocol.NodeRegisterRequest{
		Name:      name,
		NodeType:  api.NodeTypeExit,
		PublicKey: publicKey,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/nodes/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("register request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("register failed: status %d", resp.StatusCode)
	}

	var result protocol.NodeRegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode register response: %w", err)
	}

	return &result, nil
}

// SendHeartbeat sends a heartbeat with metrics to the control plane.
func (c *Client) SendHeartbeat(ctx context.Context, nodeID string, metrics api.Metrics) (*protocol.HeartbeatResponse, error) {
	body, _ := json.Marshal(protocol.HeartbeatRequest{
		NodeID:  nodeID,
		Metrics: metrics,
	})

	url := fmt.Sprintf("%s/api/v1/nodes/%s/heartbeat", c.baseURL, nodeID)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("heartbeat request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("heartbeat failed: status %d", resp.StatusCode)
	}

	var result protocol.HeartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode heartbeat response: %w", err)
	}

	return &result, nil
}

// StartHeartbeatLoop runs the heartbeat loop every 15 seconds.
func (c *Client) StartHeartbeatLoop(ctx context.Context, nodeID string, collectMetrics func() api.Metrics, onResponse func(*protocol.HeartbeatResponse)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			metrics := collectMetrics()
			resp, err := c.SendHeartbeat(ctx, nodeID, metrics)
			if err != nil {
				c.logger.Warn("heartbeat failed", zap.Error(err))
				continue
			}
			if onResponse != nil {
				onResponse(resp)
			}
		}
	}
}
