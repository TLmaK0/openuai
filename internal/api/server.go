package api

import (
	"context"
	"fmt"
	"net/http"

	"openuai/internal/logger"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// Config holds API server settings.
type Config struct {
	Port int
	Host string
}

// Handlers holds callback functions wired by the main package.
// Using func fields avoids importing main (no import cycles).
type Handlers struct {
	SendMessage          func(content string) any          // async: returns request_id
	SendMessageSync      func(content string) any          // blocking: returns ChatResponse
	GetCostSummary       func() any
	GetModels            func() any
	GetModel             func() any
	SetModel             func(model string) any
	GetProvider          func() any
	SetProvider          func(provider string) any
	GetWatchedChats      func() any
	WatchChat            func(jid string) any
	UnwatchChat          func(jid string) any
	GetEventStats        func() any
	GetMCPServers        func() any
	RespondPermission    func(level string, approved bool)
	ClearChat            func()
	ResetCosts           func()
	GetNotifications     func() any
	SetNotifications     func(enabled bool)
}

// Server is the REST API + WebSocket server.
type Server struct {
	echo     *echo.Echo
	hub      *Hub
	handlers Handlers
	config   Config
}

// New creates a new API server (does not start it).
func New(cfg Config, h Handlers) *Server {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 9120
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())

	hub := newHub()

	s := &Server{
		echo:     e,
		hub:      hub,
		handlers: h,
		config:   cfg,
	}
	s.registerRoutes()
	return s
}

// Start begins listening in a goroutine (non-blocking).
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	logger.Info("API server starting on %s", addr)
	go func() {
		if err := s.echo.Start(addr); err != nil && err != http.ErrServerClosed {
			logger.Error("API server error: %s", err.Error())
		}
	}()
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	logger.Info("API server shutting down")
	return s.echo.Shutdown(ctx)
}

// Broadcast sends an event to all connected WebSocket clients.
func (s *Server) Broadcast(event, requestID string, data any) {
	s.hub.Broadcast(event, requestID, data)
}
