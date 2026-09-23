package authcrypto

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 7 * 24 * time.Hour
)

type Claims struct {
	AdminID  string `json:"admin_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// IssueAccessToken signs a short-lived JWT identifying the admin.
func IssueAccessToken(secret, adminID, username, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		AdminID:  adminID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ParseAccessToken validates signature and expiry and returns the claims. Callers
// (httpapi.requireAdmin) treat any error as "reject with 401" without distinguishing
// why - there's no security benefit to telling a caller whether their token was
// expired vs malformed vs wrongly signed.
func ParseAccessToken(secret, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	return claims, nil
}

// IssueRefreshToken generates an opaque random token and stores it in Redis, keyed by
// its own value, mapped to the admin id, with a TTL. Revoking a session (Security PRD
// §6) is deleting this key - a follow-up story, not needed for login itself.
func IssueRefreshToken(ctx context.Context, rdb *redis.Client, adminID string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate refresh token: %w", err)
	}
	token := hex.EncodeToString(buf)

	if err := rdb.Set(ctx, refreshTokenKey(token), adminID, RefreshTokenTTL).Err(); err != nil {
		return "", fmt.Errorf("store refresh token: %w", err)
	}
	return token, nil
}

func refreshTokenKey(token string) string {
	return "refresh_token:" + token
}

// ErrInvalidRefreshToken covers both "never existed" and "expired" - Redis's own
// TTL eviction makes those indistinguishable, which is fine: either way the caller
// must log in again.
var ErrInvalidRefreshToken = errors.New("invalid or expired refresh token")

// ValidateRefreshToken looks up the admin id a refresh token maps to. Does not
// rotate or delete the token - the same refresh token remains valid until its TTL
// naturally expires (STORY-06 scope note: rotation-per-use is deferred, it needs its
// own design for concurrent-tab races rather than a rushed addition here).
func ValidateRefreshToken(ctx context.Context, rdb *redis.Client, token string) (string, error) {
	adminID, err := rdb.Get(ctx, refreshTokenKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrInvalidRefreshToken
	}
	if err != nil {
		return "", fmt.Errorf("look up refresh token: %w", err)
	}
	return adminID, nil
}
