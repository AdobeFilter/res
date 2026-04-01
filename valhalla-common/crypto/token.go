package crypto

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// TokenClaims holds JWT claims for Valhalla authentication.
type TokenClaims struct {
	AccountID string `json:"account_id"`
	NodeID    string `json:"node_id,omitempty"`
	jwt.RegisteredClaims
}

// TokenManager handles JWT creation and validation.
type TokenManager struct {
	secretKey []byte
	expiry    time.Duration
}

// NewTokenManager creates a new TokenManager with the given secret and token expiry duration.
func NewTokenManager(secret string, expiry time.Duration) *TokenManager {
	return &TokenManager{
		secretKey: []byte(secret),
		expiry:    expiry,
	}
}

// GenerateToken creates a new JWT for the given account and optional node.
func (tm *TokenManager) GenerateToken(accountID, nodeID string) (string, error) {
	now := time.Now()
	claims := TokenClaims{
		AccountID: accountID,
		NodeID:    nodeID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(tm.expiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    "valhalla",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(tm.secretKey)
}

// ValidateToken parses and validates a JWT, returning the claims.
func (tm *TokenManager) ValidateToken(tokenString string) (*TokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &TokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return tm.secretKey, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*TokenClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// RefreshToken creates a new token with the same claims but extended expiry.
func (tm *TokenManager) RefreshToken(tokenString string) (string, error) {
	claims, err := tm.ValidateToken(tokenString)
	if err != nil {
		return "", err
	}
	return tm.GenerateToken(claims.AccountID, claims.NodeID)
}
