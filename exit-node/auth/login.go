package auth

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"valhalla/common/protocol"
)

// InteractiveLogin prompts for email/password and authenticates with the control plane.
func InteractiveLogin(controlPlaneURL string) (*protocol.AuthResponse, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("Password: ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)

	return Login(controlPlaneURL, email, password)
}

// Login authenticates with the control plane.
func Login(controlPlaneURL, email, password string) (*protocol.AuthResponse, error) {
	body, _ := json.Marshal(protocol.LoginRequest{
		Email:    email,
		Password: password,
	})

	url := strings.TrimRight(controlPlaneURL, "/") + "/api/v1/auth/login"
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login failed with status %d", resp.StatusCode)
	}

	var authResp protocol.AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, fmt.Errorf("decode login response: %w", err)
	}

	return &authResp, nil
}

// SaveToken stores the JWT token to a file with restricted permissions.
func SaveToken(path, token string) error {
	return os.WriteFile(path, []byte(token), 0600)
}

// LoadToken reads a stored JWT token from file.
func LoadToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
