// Package admin — служебная админка поверх avito_listings (issue #57):
// минимальная логин-сессия из env-кредов и read-only API/таблица объявлений.
//
// TODO(#57): авторизация осознанно не связана с auth-service (его нет до
// Этапа 2). При появлении auth-service мигрировать проверку сессии туда.
package admin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookie = "admin_session"

// Sessions — HMAC-подписанные сессионные токены без серверного состояния:
// token = base64url(login | expiry | nonce | mac).
type Sessions struct {
	secret []byte
	ttl    time.Duration
}

func NewSessions(secret string, ttl time.Duration) *Sessions {
	return &Sessions{secret: []byte(secret), ttl: ttl}
}

func (s *Sessions) issue(login string, now time.Time) string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	payload := fmt.Sprintf("%s|%d|%s", login, now.Add(s.ttl).Unix(), nonce)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) +
		"." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify возвращает логин владельца валидного неистёкшего токена.
func (s *Sessions) Verify(token string, now time.Time) (string, bool) {
	payloadB64, sigB64, ok := strings.Cut(token, ".")
	if !ok {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return "", false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", false
	}
	parts := strings.SplitN(string(payload), "|", 3)
	if len(parts) != 3 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.Unix() >= exp {
		return "", false
	}
	return parts[0], true
}

// SetCookie / ClearCookie — управление сессионной cookie.
func (s *Sessions) SetCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.ttl.Seconds()),
	})
}

func (s *Sessions) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/admin",
		HttpOnly: true, MaxAge: -1,
	})
}

func (s *Sessions) FromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	return s.Verify(c.Value, time.Now())
}
