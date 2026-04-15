package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/ssh"
	"valhalla/control-plane/middleware"
)

const (
	credentialsPath  = "/etc/valhalla/credentials.txt"
	installScriptURL = "https://raw.githubusercontent.com/nicknsy/valhalla/main/exit-node/install/xray-exit.sh"
	sshTimeout       = 30 * time.Second
	commandTimeout   = 120 * time.Second
)

type SSHProxyHandler struct {
	logger *zap.Logger
}

func NewSSHProxyHandler(logger *zap.Logger) *SSHProxyHandler {
	return &SSHProxyHandler{logger: logger}
}

type sshSetupRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type sshSetupResponse struct {
	ShareLink string `json:"share_link"`
}

// Setup handles POST /api/v1/ssh/setup
// Connects to a remote server via SSH, installs xray if needed, and returns the VLESS share link.
func (h *SSHProxyHandler) Setup(w http.ResponseWriter, r *http.Request) {
	accountID := middleware.GetAccountID(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req sshSetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Host == "" || req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "host, username, and password are required")
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}

	h.logger.Info("ssh setup requested",
		zap.String("account_id", accountID),
		zap.String("host", req.Host),
		zap.Int("port", req.Port),
		zap.String("username", req.Username),
	)

	shareLink, err := h.runSSHSetup(req)
	if err != nil {
		h.logger.Error("ssh setup failed",
			zap.String("host", req.Host),
			zap.Error(err),
		)
		writeError(w, http.StatusBadGateway, fmt.Sprintf("ssh setup failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, sshSetupResponse{ShareLink: shareLink})
}

func (h *SSHProxyHandler) runSSHSetup(req sshSetupRequest) (string, error) {
	config := &ssh.ClientConfig{
		User: req.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(req.Password),
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = req.Password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         sshTimeout,
	}

	addr := net.JoinHostPort(req.Host, fmt.Sprintf("%d", req.Port))
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer client.Close()

	// 1. Try reading existing credentials
	shareLink, _ := h.readShareLink(client)
	if shareLink != "" {
		return shareLink, nil
	}

	// 2. Install xray
	_, err = h.execCommand(client, fmt.Sprintf("curl -sL %s | bash", installScriptURL))
	if err != nil {
		return "", fmt.Errorf("install: %w", err)
	}

	// 3. Read credentials after install
	shareLink, err = h.readShareLink(client)
	if err != nil {
		return "", fmt.Errorf("read credentials: %w", err)
	}
	if shareLink == "" {
		return "", fmt.Errorf("no vless share link found after installation")
	}

	return shareLink, nil
}

func (h *SSHProxyHandler) readShareLink(client *ssh.Client) (string, error) {
	output, err := h.execCommand(client, fmt.Sprintf("cat %s 2>/dev/null", credentialsPath))
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "SHARE_LINK=") {
			link := strings.TrimSpace(strings.TrimPrefix(line, "SHARE_LINK="))
			if strings.HasPrefix(link, "vless://") {
				return link, nil
			}
		}
	}
	return "", nil
}

func (h *SSHProxyHandler) execCommand(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case err := <-done:
		if err != nil && stdout.Len() == 0 {
			return "", fmt.Errorf("command failed: %w (stderr: %s)", err, stderr.String())
		}
		return stdout.String(), nil
	case <-time.After(commandTimeout):
		return "", fmt.Errorf("command timed out after %v", commandTimeout)
	}
}
