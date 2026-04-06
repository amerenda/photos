package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	adminCookie  = "photos_admin"
	secretCookie = "photos_secret"
	adminTTL     = 7 * 24 * time.Hour
	secretTTL    = 24 * time.Hour
)

var (
	githubClientID     = os.Getenv("GITHUB_CLIENT_ID")
	githubClientSecret = os.Getenv("GITHUB_CLIENT_SECRET")
	sessionSecret      = []byte(os.Getenv("SESSION_SECRET"))
	allowedUsers       = parseAllowed(os.Getenv("GITHUB_ALLOWED_USERS"))
)

func parseAllowed(s string) map[string]bool {
	m := make(map[string]bool)
	for _, u := range strings.Split(s, ",") {
		u = strings.TrimSpace(u)
		if u != "" {
			m[u] = true
		}
	}
	if len(m) == 0 {
		m["amerenda"] = true
	}
	return m
}

// makeToken creates a signed token: base64(payload) + "." + base64(hmac)
func makeToken(subject string, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	payload := subject + "|" + exp
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// verifyToken returns the subject if valid and unexpired, else "".
func verifyToken(token string) string {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return ""
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	payload := string(payloadBytes)

	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write([]byte(payload))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return ""
	}

	idx := strings.LastIndex(payload, "|")
	if idx < 0 {
		return ""
	}
	expUnix, err := strconv.ParseInt(payload[idx+1:], 10, 64)
	if err != nil || time.Now().Unix() > expUnix {
		return ""
	}
	return payload[:idx]
}

// GetAdminUser returns the authenticated admin username, or "".
func GetAdminUser(r *http.Request) string {
	c, err := r.Cookie(adminCookie)
	if err != nil {
		return ""
	}
	return verifyToken(c.Value)
}

// IsSecretAuthed returns true if the request has a valid secret album session.
func IsSecretAuthed(r *http.Request) bool {
	c, err := r.Cookie(secretCookie)
	if err != nil {
		return false
	}
	return verifyToken(c.Value) == "secret"
}

// SetAdminCookie writes the admin session cookie.
func SetAdminCookie(w http.ResponseWriter, username string) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookie,
		Value:    makeToken(username, adminTTL),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(adminTTL.Seconds()),
	})
}

// SetSecretCookie writes the secret album session cookie.
func SetSecretCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     secretCookie,
		Value:    makeToken("secret", secretTTL),
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(secretTTL.Seconds()),
	})
}

// ClearCookies clears all session cookies.
func ClearCookies(w http.ResponseWriter) {
	for _, name := range []string{adminCookie, secretCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:   name,
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
	}
}

// RequireAdmin wraps a handler, redirecting to /auth/login if not authed.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if GetAdminUser(r) == "" {
			http.Redirect(w, r, "/auth/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// RequireSecret wraps a handler, redirecting to /s if not authed.
func RequireSecret(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !IsSecretAuthed(r) {
			http.Redirect(w, r, "/s", http.StatusFound)
			return
		}
		next(w, r)
	}
}

// LoginHandler redirects the browser to GitHub OAuth.
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	url := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&scope=read:user",
		githubClientID,
	)
	http.Redirect(w, r, url, http.StatusFound)
}

// CallbackHandler handles the GitHub OAuth callback.
func CallbackHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	username, err := exchangeCodeForUser(code)
	if err != nil {
		log.Printf("github oauth error: %v", err)
		http.Error(w, "authentication failed", http.StatusUnauthorized)
		return
	}

	if !allowedUsers[username] {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	SetAdminCookie(w, username)
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func exchangeCodeForUser(code string) (string, error) {
	// Exchange code for access token
	reqBody := fmt.Sprintf(
		`{"client_id":%q,"client_secret":%q,"code":%q}`,
		githubClientID, githubClientSecret, code,
	)
	req, _ := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.AccessToken == "" {
		return "", fmt.Errorf("no access_token: %s", body)
	}

	// Get user info
	req2, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req2.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	req2.Header.Set("Accept", "application/json")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)

	var user struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body2, &user); err != nil || user.Login == "" {
		return "", fmt.Errorf("no login in user response: %s", body2)
	}
	return user.Login, nil
}
