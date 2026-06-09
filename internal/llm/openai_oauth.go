package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	oauthClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	oauthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	oauthTokenURL    = "https://auth.openai.com/oauth/token"
	oauthRedirectURI = "http://localhost:1455/auth/callback"
	oauthScope       = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	oauthCallbackPort = 1455
)

type OAuthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	AccountID    string `json:"account_id"`
}

type OAuthFlow struct {
	mu             sync.Mutex
	tokens         *OAuthTokens
	onTokensUpdate func(tokens *OAuthTokens)
	activeServer   *http.Server
}

func NewOAuthFlow(onUpdate func(tokens *OAuthTokens)) *OAuthFlow {
	return &OAuthFlow{onTokensUpdate: onUpdate}
}

func (o *OAuthFlow) SetTokens(tokens *OAuthTokens) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.tokens = tokens
}

func (o *OAuthFlow) GetAccessToken() (string, string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.tokens == nil {
		return "", "", fmt.Errorf("not authenticated, please login first")
	}

	if time.Now().Unix() >= o.tokens.ExpiresAt-60 {
		if err := o.refreshLocked(); err != nil {
			return "", "", fmt.Errorf("token refresh failed: %w", err)
		}
	}

	return o.tokens.AccessToken, o.tokens.AccountID, nil
}

func (o *OAuthFlow) IsAuthenticated() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tokens != nil && o.tokens.AccessToken != ""
}

// Login starts the OAuth flow: opens browser, waits for callback
func (o *OAuthFlow) Login() error {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return fmt.Errorf("generate PKCE: %w", err)
	}

	state, err := randomHex(16)
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	authURL := buildAuthorizeURL(state, challenge)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	// Tear down any callback server left listening by a previous, abandoned
	// login attempt — otherwise binding :1455 fails with "address already in use".
	o.mu.Lock()
	if o.activeServer != nil {
		o.activeServer.Close()
		o.activeServer = nil
	}
	o.mu.Unlock()

	server, err := startCallbackServer(state, codeCh, errCh)
	if err != nil {
		return fmt.Errorf("start callback server: %w", err)
	}

	o.mu.Lock()
	o.activeServer = server
	o.mu.Unlock()

	// Guarantee the server is closed on every return path so the port is freed
	// for the next attempt (the error path used to leak it).
	defer func() {
		server.Close()
		o.mu.Lock()
		if o.activeServer == server {
			o.activeServer = nil
		}
		o.mu.Unlock()
	}()

	if err := openBrowser(authURL); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("login timed out after 5 minutes")
	}

	tokens, err := exchangeCode(code, verifier)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}

	o.mu.Lock()
	o.tokens = tokens
	o.mu.Unlock()

	if o.onTokensUpdate != nil {
		o.onTokensUpdate(tokens)
	}

	return nil
}

func (o *OAuthFlow) refreshLocked() error {
	if o.tokens == nil || o.tokens.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {o.tokens.RefreshToken},
		"client_id":     {oauthClientID},
	}

	resp, err := http.PostForm(oauthTokenURL, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh failed with status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	accountID := extractAccountID(result.AccessToken)
	if accountID == "" {
		accountID = o.tokens.AccountID
	}

	o.tokens = &OAuthTokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().Unix() + result.ExpiresIn,
		AccountID:    accountID,
	}

	if o.onTokensUpdate != nil {
		o.onTokensUpdate(o.tokens)
	}

	return nil
}

func exchangeCode(code, verifier string) (*OAuthTokens, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {oauthRedirectURI},
	}

	resp, err := http.PostForm(oauthTokenURL, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed with status %d", resp.StatusCode)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	accountID := extractAccountID(result.AccessToken)

	return &OAuthTokens{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    time.Now().Unix() + result.ExpiresIn,
		AccountID:    accountID,
	}, nil
}

func extractAccountID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}

	payload := parts[1]
	// Fix base64url padding
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}

	// Account ID is in the custom claim. ChatGPT-account tokens expose it as
	// "chatgpt_account_id"; some org tokens use "organization_id". Prefer the
	// ChatGPT account id (required by the Codex backend), fall back to org id.
	if authClaim, ok := claims["https://api.openai.com/auth"].(map[string]interface{}); ok {
		if acctID, ok := authClaim["chatgpt_account_id"].(string); ok && acctID != "" {
			return acctID
		}
		if orgID, ok := authClaim["organization_id"].(string); ok && orgID != "" {
			return orgID
		}
	}

	return ""
}

func startCallbackServer(expectedState string, codeCh chan<- string, errCh chan<- error) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		if state != expectedState {
			errCh <- fmt.Errorf("state mismatch")
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback")
			http.Error(w, "No code", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h2>Login successful!</h2><p>You can close this window.</p><script>window.close()</script></body></html>`)
		codeCh <- code
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", oauthCallbackPort),
		Handler: mux,
	}

	// Bind synchronously so a port-in-use error surfaces to the caller as a
	// clean error instead of being raced onto errCh after the browser opens.
	ln, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return nil, err
	}

	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("callback server: %w", err)
		}
	}()

	return server, nil
}

func buildAuthorizeURL(state, challenge string) string {
	// Match the codex CLI's authorize request byte-for-byte: same parameter
	// order and RFC 3986 percent-encoding (space -> %20). net/url's Encode()
	// sorts params and emits "+" for spaces, which the OpenAI authorize
	// endpoint rejects with authorize_hydra_invalid_request (it treats the
	// "+"-joined scope as a single invalid scope token).
	params := [][2]string{
		{"response_type", "code"},
		{"client_id", oauthClientID},
		{"redirect_uri", oauthRedirectURI},
		{"scope", oauthScope},
		{"code_challenge", challenge},
		{"code_challenge_method", "S256"},
		{"id_token_add_organizations", "true"},
		{"codex_cli_simplified_flow", "true"},
		{"state", state},
		{"originator", "codex_cli_rs"},
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		v := strings.ReplaceAll(url.QueryEscape(p[1]), "+", "%20")
		parts = append(parts, p[0]+"="+v)
	}
	return oauthAuthorizeURL + "?" + strings.Join(parts, "&")
}

func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// Do NOT use `cmd /c start <url>`: cmd.exe treats the "&" that
		// separates query parameters as a command separator, so the browser
		// only receives the URL up to the first "&" (dropping client_id,
		// scope, etc.) and the OAuth authorize request fails with
		// missing_required_parameter. rundll32 receives the whole URL as a
		// single argument with no shell parsing.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
