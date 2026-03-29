package mcpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"

	"openuai/internal/logger"
)

const callbackPort = 19120
const callbackPath = "/oauth/callback"

// RunOAuthFlow performs the full OAuth authorization code flow with PKCE:
// 1. Registers client dynamically if needed
// 2. Starts a local HTTP server for the callback
// 3. Opens the browser with the authorization URL
// 4. Waits for the callback with the authorization code
// 5. Exchanges the code for tokens
func RunOAuthFlow(ctx context.Context, handler *transport.OAuthHandler) error {
	// Register client dynamically if no client ID
	if handler.GetClientID() == "" {
		logger.Info("OAuth: registering client dynamically")
		if err := handler.RegisterClient(ctx, "OpenUAI"); err != nil {
			logger.Error("OAuth: dynamic registration failed: %s", err)
			// Continue anyway — some servers don't support dynamic registration
			// and expect a pre-configured client_id
		}
	}

	// Generate PKCE verifier + challenge
	codeVerifier, err := transport.GenerateCodeVerifier()
	if err != nil {
		return fmt.Errorf("generate code verifier: %w", err)
	}
	codeChallenge := transport.GenerateCodeChallenge(codeVerifier)

	// Generate state for CSRF protection
	state, err := transport.GenerateState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}

	// Get authorization URL
	authURL, err := handler.GetAuthorizationURL(ctx, state, codeChallenge)
	if err != nil {
		return fmt.Errorf("get authorization URL: %w", err)
	}

	logger.Info("OAuth: authorization URL: %s", authURL)

	// Start callback server
	codeCh := make(chan callbackResult, 1)
	srv := startCallbackServer(codeCh)
	defer srv.Shutdown(context.Background())

	// Open browser
	if err := openBrowser(authURL); err != nil {
		logger.Error("OAuth: failed to open browser: %s", err)
		return fmt.Errorf("failed to open browser: %w (URL: %s)", err, authURL)
	}

	// Wait for callback
	logger.Info("OAuth: waiting for callback on port %d...", callbackPort)
	select {
	case result := <-codeCh:
		if result.err != "" {
			return fmt.Errorf("OAuth error: %s (%s)", result.errDescription, result.err)
		}
		logger.Info("OAuth: received authorization code, exchanging for token...")
		if err := handler.ProcessAuthorizationResponse(ctx, result.code, result.state, codeVerifier); err != nil {
			return fmt.Errorf("token exchange failed: %w", err)
		}
		logger.Info("OAuth: authentication successful")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("OAuth timeout: no callback received within 5 minutes")
	}
}

type callbackResult struct {
	code           string
	state          string
	err            string
	errDescription string
}

func startCallbackServer(codeCh chan<- callbackResult) *http.Server {
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			q := r.URL.Query()
			if errCode := q.Get("error"); errCode != "" {
				codeCh <- callbackResult{
					err:            errCode,
					errDescription: q.Get("error_description"),
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprintf(w, "<html><body><h2>Authentication failed</h2><p>%s</p><script>window.close()</script></body></html>", q.Get("error_description"))
				return
			}
			codeCh <- callbackResult{
				code:  q.Get("code"),
				state: q.Get("state"),
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h2>Authentication successful!</h2><p>You can close this tab.</p><script>window.close()</script></body></html>")
		})
	})

	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", callbackPort),
		Handler: mux,
	}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		logger.Error("OAuth: failed to listen on %s: %s", srv.Addr, err)
		return srv
	}

	go srv.Serve(ln)
	logger.Info("OAuth: callback server listening on %s", srv.Addr)
	return srv
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}
