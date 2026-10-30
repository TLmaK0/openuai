package mcpserver

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/mark3labs/mcp-go/server"
)

// Path is where the MCP endpoint is mounted on the local API.
const Path = "/mcp"

// HTTPHandler serves the MCP server over streamable HTTP. It only accepts
// requests addressed to a loopback host and, when the request comes from a
// browser, from a loopback origin: the API listens on 127.0.0.1, and this
// stops web pages from reaching it through DNS rebinding or cross-site
// requests.
func HTTPHandler(s *server.MCPServer) http.Handler {
	h := server.NewStreamableHTTPServer(s, server.WithEndpointPath(Path))
	return LocalOnly(h)
}

// LocalOnly rejects requests whose Host or Origin is not a loopback address.
func LocalOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden: host must be localhost", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !isLoopbackHost(u.Host) {
				http.Error(w, "forbidden: origin not allowed", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
