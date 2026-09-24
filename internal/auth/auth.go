package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"reporting-server/internal/apiclients"
)

const (
	sessionCookie = "report_session"
	stateCookie   = "report_oidc_state"
)

type User struct {
	Subject   string   `json:"subject"`
	Email     string   `json:"email,omitempty"`
	Name      string   `json:"name"`
	Groups    []string `json:"groups,omitempty"`
	Admin     bool     `json:"admin"`
	Service   bool     `json:"service,omitempty"`
	ClientID  string   `json:"clientId,omitempty"`
	KeyID     string   `json:"keyId,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	ExpiresAt int64    `json:"expiresAt"`
}

type Config struct {
	Development   bool
	IssuerURL     string
	ClientID      string
	ClientSecret  string
	RedirectURL   string
	AdminGroup    string
	SessionSecret string
}

type MachineAuthenticator interface {
	Authenticate(context.Context, string, string) (apiclients.Identity, error)
}

type Authenticator struct {
	development  bool
	oauth        oauth2.Config
	verifier     *oidc.IDTokenVerifier
	adminGroup   string
	secret       []byte
	machine      MachineAuthenticator
	secureCookie bool
}

type contextKey struct{}

type loginState struct {
	State     string `json:"state"`
	Verifier  string `json:"verifier"`
	ExpiresAt int64  `json:"expiresAt"`
}

func New(ctx context.Context, config Config, machine MachineAuthenticator) (*Authenticator, error) {
	authenticator := &Authenticator{
		development:  config.Development,
		adminGroup:   config.AdminGroup,
		secret:       []byte(config.SessionSecret),
		machine:      machine,
		secureCookie: !config.Development,
	}
	if config.Development {
		return authenticator, nil
	}
	provider, err := oidc.NewProvider(ctx, config.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	authenticator.oauth = oauth2.Config{
		ClientID: config.ClientID, ClientSecret: config.ClientSecret,
		Endpoint: provider.Endpoint(), RedirectURL: config.RedirectURL,
		Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
	authenticator.verifier = provider.Verifier(&oidc.Config{ClientID: config.ClientID})
	return authenticator, nil
}

func (a *Authenticator) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", a.login)
	mux.HandleFunc("GET /auth/callback", a.callback)
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.Handle("GET /auth/me", a.RequireUser(http.HandlerFunc(a.me)))
}

func (a *Authenticator) RequireUser(next http.Handler) http.Handler {
	return a.authorize(next, false, "")
}

func (a *Authenticator) RequireAdmin(next http.Handler) http.Handler {
	return a.authorize(next, true, "")
}

func (a *Authenticator) AllowServiceOrUser(scope string, next http.Handler) http.Handler {
	return a.authorize(next, false, scope)
}

func (a *Authenticator) AllowServiceOrAdmin(scope string, next http.Handler) http.Handler {
	return a.authorize(next, true, scope)
}

func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(contextKey{}).(User)
	return user, ok
}

func (a *Authenticator) authorize(next http.Handler, adminRequired bool, machineScope string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if a.development {
			user := User{Subject: "development", Name: "Development Admin", Admin: true, ExpiresAt: time.Now().Add(24 * time.Hour).Unix()}
			next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), contextKey{}, user)))
			return
		}
		if machineScope != "" {
			provided, present := machineKey(request)
			if present {
				identity, err := a.machine.Authenticate(request.Context(), provided, remoteIP(request))
				if err != nil {
					writeAuthError(response, http.StatusUnauthorized, "invalid_api_key", "API key is invalid")
					return
				}
				if !slices.Contains(identity.Scopes, machineScope) {
					writeAuthError(response, http.StatusForbidden, "insufficient_scope", "API key does not grant "+machineScope)
					return
				}
				user := User{Subject: "api-client:" + identity.ClientID, Name: identity.ClientName, Service: true, ClientID: identity.ClientID, KeyID: identity.KeyID, Scopes: identity.Scopes, ExpiresAt: time.Now().Add(time.Minute).Unix()}
				next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), contextKey{}, user)))
				return
			}
		}
		user, err := a.sessionUser(request)
		if err != nil {
			writeAuthError(response, http.StatusUnauthorized, "authentication_required", "Sign in with Authentik")
			return
		}
		if adminRequired && !user.Admin {
			writeAuthError(response, http.StatusForbidden, "forbidden", "Report administrator access is required")
			return
		}
		next.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), contextKey{}, user)))
	})
}

func (a *Authenticator) login(response http.ResponseWriter, request *http.Request) {
	if a.development {
		http.Redirect(response, request, "/", http.StatusFound)
		return
	}
	state, err := randomToken()
	if err != nil {
		http.Error(response, "Could not start login", http.StatusInternalServerError)
		return
	}
	verifier := oauth2.GenerateVerifier()
	payload := loginState{State: state, Verifier: verifier, ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	a.setCookie(response, stateCookie, a.encode(payload), 10*time.Minute)
	location := a.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	http.Redirect(response, request, location, http.StatusFound)
}

func (a *Authenticator) callback(response http.ResponseWriter, request *http.Request) {
	if providerError := request.URL.Query().Get("error"); providerError != "" {
		http.Error(response, "Authentik login failed: "+providerError, http.StatusUnauthorized)
		return
	}
	cookie, err := request.Cookie(stateCookie)
	if err != nil {
		http.Error(response, "Login state is missing", http.StatusBadRequest)
		return
	}
	var state loginState
	if err := a.decode(cookie.Value, &state); err != nil || state.ExpiresAt < time.Now().Unix() || request.URL.Query().Get("state") != state.State {
		http.Error(response, "Login state is invalid", http.StatusBadRequest)
		return
	}
	token, err := a.oauth.Exchange(request.Context(), request.URL.Query().Get("code"), oauth2.VerifierOption(state.Verifier))
	if err != nil {
		slog.Warn("OIDC authorization code exchange failed", "error", err, "redirect_url", a.oauth.RedirectURL)
		http.Error(response, "Could not exchange login code", http.StatusUnauthorized)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Error(response, "Identity token is missing", http.StatusUnauthorized)
		return
	}
	idToken, err := a.verifier.Verify(request.Context(), rawIDToken)
	if err != nil {
		http.Error(response, "Identity token is invalid", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Subject           string   `json:"sub"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(response, "Identity claims are invalid", http.StatusUnauthorized)
		return
	}
	name := claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	if name == "" {
		name = claims.Email
	}
	if name == "" {
		name = claims.Subject
	}
	expiresAt := idToken.Expiry
	maximumExpiry := time.Now().Add(8 * time.Hour)
	if expiresAt.After(maximumExpiry) {
		expiresAt = maximumExpiry
	}
	user := User{
		Subject: claims.Subject, Email: claims.Email, Name: name, Groups: claims.Groups,
		Admin: slices.Contains(claims.Groups, a.adminGroup), ExpiresAt: expiresAt.Unix(),
	}
	a.setCookie(response, sessionCookie, a.encode(user), time.Until(expiresAt))
	a.clearCookie(response, stateCookie)
	http.Redirect(response, request, "/", http.StatusFound)
}

func (a *Authenticator) logout(response http.ResponseWriter, request *http.Request) {
	a.clearCookie(response, sessionCookie)
	response.WriteHeader(http.StatusNoContent)
}

func (a *Authenticator) me(response http.ResponseWriter, request *http.Request) {
	user, _ := UserFromContext(request.Context())
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(user)
}

func (a *Authenticator) sessionUser(request *http.Request) (User, error) {
	cookie, err := request.Cookie(sessionCookie)
	if err != nil {
		return User{}, err
	}
	var user User
	if err := a.decode(cookie.Value, &user); err != nil {
		return User{}, err
	}
	if user.Subject == "" || user.ExpiresAt < time.Now().Unix() {
		return User{}, errors.New("session expired")
	}
	return user, nil
}

func machineKey(request *http.Request) (string, bool) {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if authorization != "" {
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return authorization, true
		}
		return parts[1], true
	}
	value := strings.TrimSpace(request.Header.Get("X-API-Key"))
	return value, value != ""
}

func remoteIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil {
		return host
	}
	return ""
}

func (a *Authenticator) encode(value any) string {
	payload, _ := json.Marshal(value)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *Authenticator) decode(value string, target any) error {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return errors.New("invalid signed value")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func (a *Authenticator) setCookie(response http.ResponseWriter, name, value string, lifetime time.Duration) {
	http.SetCookie(response, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.secureCookie,
		SameSite: http.SameSiteLaxMode, MaxAge: int(lifetime.Seconds()),
	})
}

func (a *Authenticator) clearCookie(response http.ResponseWriter, name string) {
	http.SetCookie(response, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func writeAuthError(response http.ResponseWriter, status int, code, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
