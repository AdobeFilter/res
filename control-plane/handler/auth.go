package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"go.uber.org/zap"
	"valhalla/common/crypto"
	"valhalla/common/protocol"
	"valhalla/control-plane/db"
	"valhalla/control-plane/remnawave"

	"golang.org/x/crypto/bcrypt"
)

// emailRe mirrors Remnawave's email validator (Zod's z.string().email())
// closely enough that anything we accept will also pass on the panel side —
// single-letter TLDs in particular ("x@c.l") are rejected up front instead of
// after we've already inserted the row in our DB. Kept in sync with the
// Android client's isValidEmail regex in AuthComponents.kt.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[A-Za-z]{2,}$`)

type AuthHandler struct {
	accounts  db.AccountRepository
	tokens    *crypto.TokenManager
	remnawave *remnawave.Client
	logger    *zap.Logger
}

func NewAuthHandler(accounts db.AccountRepository, tokens *crypto.TokenManager, rw *remnawave.Client, logger *zap.Logger) *AuthHandler {
	return &AuthHandler{accounts: accounts, tokens: tokens, remnawave: rw, logger: logger}
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

	if !emailRe.MatchString(req.Email) {
		writeError(w, http.StatusBadRequest, "invalid email format")
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

	// Provision a Remnawave user with the free-tier monthly quota and persist
	// the link. When Remnawave is configured (i.e. we're in subscription mode,
	// not standalone mesh), provisioning is treated as PART of registration —
	// if it fails, we roll back the account so the next register attempt is
	// clean (no 409 lurking) and the caller knows something is wrong. When
	// Remnawave isn't configured, we silently skip.
	if h.remnawave.Enabled() {
		username := remnawaveUsername(account.ID)
		user, rwErr := h.remnawave.CreateUser(username, req.Email, remnawave.FreeTierBytes)
		if rwErr != nil {
			h.logger.Error("remnawave provisioning failed — rolling back account",
				zap.String("account_id", account.ID),
				zap.String("email", req.Email),
				zap.Error(rwErr))
			if delErr := h.accounts.Delete(r.Context(), account.ID); delErr != nil {
				h.logger.Error("rollback delete failed — orphan account left",
					zap.String("account_id", account.ID), zap.Error(delErr))
			}
			writeError(w, http.StatusBadGateway, "subscription provisioning failed; try again")
			return
		}
		if err := h.accounts.SetRemnawaveLink(r.Context(), account.ID, user.UUID, user.SubscriptionURL); err != nil {
			// Soft failure — Remnawave user exists, our DB write missed. The
			// reprovision endpoint can re-link from the panel side using
			// username. Don't roll back the account.
			h.logger.Error("save remnawave link failed", zap.String("account_id", account.ID), zap.Error(err))
		}
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

// remnawaveUsername builds an alphanumeric+underscore Remnawave username
// from our account UUID. Remnawave constrains usernames; stripping dashes
// gives a 32-char hex string that's safe across versions.
func remnawaveUsername(accountID string) string {
	return "v_" + strings.ReplaceAll(accountID, "-", "")
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
