package api

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/labstack/echo/v4"
)

func (s *Server) registerRoutes() {
	api := s.echo.Group("/api")

	api.POST("/chat", s.handleChat)
	api.POST("/chat/clear", s.handleClearChat)
	api.GET("/cost", s.handleGetCost)
	api.POST("/costs/reset", s.handleResetCosts)
	api.GET("/models", s.handleGetModels)
	api.GET("/model", s.handleGetModel)
	api.PUT("/model", s.handleSetModel)
	api.GET("/provider", s.handleGetProvider)
	api.PUT("/provider", s.handleSetProvider)
	api.GET("/chats/watched", s.handleGetWatchedChats)
	api.POST("/chats/watched", s.handleWatchChat)
	api.DELETE("/chats/watched/:jid", s.handleUnwatchChat)
	api.GET("/events/stats", s.handleGetEventStats)
	api.GET("/mcp/servers", s.handleGetMCPServers)
	api.POST("/permissions/respond", s.handleRespondPermission)
	api.GET("/notifications", s.handleGetNotifications)
	api.PUT("/notifications", s.handleSetNotifications)

	s.echo.GET("/ws", s.hub.handleWS)
}

// --- Chat ---

type chatRequest struct {
	Content string `json:"content"`
}

func (s *Server) handleChat(c echo.Context) error {
	var req chatRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	if req.Content == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "content is required"})
	}

	// Blocking mode
	if c.QueryParam("wait") == "true" {
		result := s.handlers.SendMessageSync(req.Content)
		return c.JSON(http.StatusOK, result)
	}

	// Async mode: return request_id immediately
	requestID := nextRequestID()
	go func() {
		result := s.handlers.SendMessageSync(req.Content)
		s.hub.Broadcast("chat_complete", requestID, result)
	}()

	return c.JSON(http.StatusAccepted, map[string]string{"request_id": requestID})
}

func (s *Server) handleClearChat(c echo.Context) error {
	s.handlers.ClearChat()
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// --- Cost ---

func (s *Server) handleGetCost(c echo.Context) error {
	return c.JSON(http.StatusOK, s.handlers.GetCostSummary())
}

func (s *Server) handleResetCosts(c echo.Context) error {
	s.handlers.ResetCosts()
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// --- Models ---

func (s *Server) handleGetModels(c echo.Context) error {
	return c.JSON(http.StatusOK, s.handlers.GetModels())
}

func (s *Server) handleGetModel(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"model": s.handlers.GetModel()})
}

type setModelRequest struct {
	Model string `json:"model"`
}

func (s *Server) handleSetModel(c echo.Context) error {
	var req setModelRequest
	if err := c.Bind(&req); err != nil || req.Model == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "model is required"})
	}
	result := s.handlers.SetModel(req.Model)
	return c.JSON(http.StatusOK, result)
}

// --- Provider ---

func (s *Server) handleGetProvider(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"provider": s.handlers.GetProvider()})
}

type setProviderRequest struct {
	Provider string `json:"provider"`
}

func (s *Server) handleSetProvider(c echo.Context) error {
	var req setProviderRequest
	if err := c.Bind(&req); err != nil || req.Provider == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "provider is required"})
	}
	result := s.handlers.SetProvider(req.Provider)
	return c.JSON(http.StatusOK, result)
}

// --- Watched Chats ---

func (s *Server) handleGetWatchedChats(c echo.Context) error {
	return c.JSON(http.StatusOK, s.handlers.GetWatchedChats())
}

type watchChatRequest struct {
	JID string `json:"jid"`
}

func (s *Server) handleWatchChat(c echo.Context) error {
	var req watchChatRequest
	if err := c.Bind(&req); err != nil || req.JID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "jid is required"})
	}
	result := s.handlers.WatchChat(req.JID)
	return c.JSON(http.StatusOK, result)
}

func (s *Server) handleUnwatchChat(c echo.Context) error {
	jid := c.Param("jid")
	if jid == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "jid is required"})
	}
	result := s.handlers.UnwatchChat(jid)
	return c.JSON(http.StatusOK, result)
}

// --- Events ---

func (s *Server) handleGetEventStats(c echo.Context) error {
	return c.JSON(http.StatusOK, s.handlers.GetEventStats())
}

// --- MCP ---

func (s *Server) handleGetMCPServers(c echo.Context) error {
	return c.JSON(http.StatusOK, s.handlers.GetMCPServers())
}

// --- Permissions ---

type permissionRequest struct {
	Level    string `json:"level"`
	Approved bool   `json:"approved"`
}

func (s *Server) handleRespondPermission(c echo.Context) error {
	var req permissionRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	s.handlers.RespondPermission(req.Level, req.Approved)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// --- Notifications ---

func (s *Server) handleGetNotifications(c echo.Context) error {
	return c.JSON(http.StatusOK, s.handlers.GetNotifications())
}

type notificationsRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleSetNotifications(c echo.Context) error {
	var req notificationsRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}
	s.handlers.SetNotifications(req.Enabled)
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// --- Helpers ---

var reqCounter atomic.Int64

func nextRequestID() string {
	n := reqCounter.Add(1)
	return fmt.Sprintf("req_%d", n)
}
