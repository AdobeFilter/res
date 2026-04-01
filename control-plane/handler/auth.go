package handler

import (
	"encoding/json"
	"net/http"

	"go.uber.org/zap"
	"valhalla/common/crypto"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"

	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	accounts db.AccountRepository
	tokens   *crypto.TokenManager
	logger   *zap.Logger
}

func NewAuthHandler(accounts db.AccountRepository, tokens *crypto.TokenManager, logger *zap.Logger) *AuthHandler {
	return &AuthHandler{accounts: accounts, tokens: tokens, logger: logger}
}

// Register handles POST /api/v1/auth/register
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req protocol.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}

	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		h.logger.Error("bcrypt hash failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	account, err := h.accounts.Create(r.Context(), req.Email, string(hash))
	if err != nil {
		h.logger.Error("create account failed", zap.Error(err))
		writeError(w, http.StatusConflict, "account already exists")
		return
	}

	token, err := h.tokens.GenerateToken(account.ID, "")
	if err != nil {
		h.logger.Error("generate token failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, protocol.AuthResponse{
		Token:     token,
		AccountID: account.ID,
	})
}

// Login handles POST /api/v1/auth/login
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req protocol.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	account, err := h.accounts.GetByEmail(r.Context(), req.Email)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(req.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	token, err := h.tokens.GenerateToken(account.ID, "")
	if err != nil {
		h.logger.Error("generate token failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, protocol.AuthResponse{
		Token:     token,
		AccountID: account.ID,
	})
}

// Refresh handles POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	if len(authHeader) < 8 {
		writeError(w, http.StatusUnauthorized, "missing token")
		return
	}
	tokenStr := authHeader[7:] // strip "Bearer "

	newToken, err := h.tokens.RefreshToken(tokenStr)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}

	claims, _ := h.tokens.ValidateToken(newToken)
	writeJSON(w, http.StatusOK, protocol.AuthResponse{
		Token:     newToken,
		AccountID: claims.AccountID,
	})
}
