package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fnfbraga/msgraph-mcpgo/internal/attachments"
	"github.com/fnfbraga/msgraph-mcpgo/internal/auth"
	"github.com/fnfbraga/msgraph-mcpgo/internal/msgraph"
	"github.com/fnfbraga/msgraph-mcpgo/internal/observability"
	apperrors "github.com/fnfbraga/msgraph-mcpgo/pkg/errors"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"github.com/sony/gobreaker"
)

// contextKey is the type for context keys
type contextKey string

const (
	// GraphClientKey is the context key for the Graph client
	GraphClientKey contextKey = "graph_client"
	// TokenKey is the context key for the access token
	TokenKey contextKey = "access_token"
)

// Server is the MCP server implementation
type Server struct {
	mcpServer           *server.MCPServer
	tokenValidator      *auth.TokenValidator
	oboExchanger        *auth.OBOExchanger
	graphClientConfig   msgraph.ClientConfig
	attachmentExtractor *attachments.Extractor
	circuitBreaker      *gobreaker.CircuitBreaker
	logger              *zerolog.Logger
	metrics             *observability.Metrics
	disableAuth         bool
	skipTokenValidation bool
}

// ServerConfig holds configuration for creating an MCP server
type ServerConfig struct {
	TokenValidator      *auth.TokenValidator
	OBOExchanger        *auth.OBOExchanger
	AttachmentExtractor *attachments.Extractor
	CircuitBreaker      *gobreaker.CircuitBreaker
	Logger              *zerolog.Logger
	Metrics             *observability.Metrics
	GraphTimeout        time.Duration
	DisableAuth         bool
	SkipTokenValidation bool
}

// NewServer creates a new MCP server
func NewServer(cfg ServerConfig) (*Server, error) {
	s := &Server{
		tokenValidator:      cfg.TokenValidator,
		oboExchanger:        cfg.OBOExchanger,
		attachmentExtractor: cfg.AttachmentExtractor,
		circuitBreaker:      cfg.CircuitBreaker,
		logger:              cfg.Logger,
		metrics:             cfg.Metrics,
		disableAuth:         cfg.DisableAuth,
		skipTokenValidation: cfg.SkipTokenValidation,
		graphClientConfig: msgraph.ClientConfig{
			Logger:         cfg.Logger,
			Metrics:        cfg.Metrics,
			CircuitBreaker: cfg.CircuitBreaker,
			Timeout:        cfg.GraphTimeout,
		},
	}

	// Create MCP server with capabilities
	mcpServer := server.NewMCPServer(
		"MS Graph MCP Server",
		"1.0.0",
		server.WithResourceCapabilities(false, true),
		server.WithLogging(),
	)

	// Register tools
	tools := DefineMCPTools()
	for _, tool := range tools {
		mcpServer.AddTool(tool, s.createToolHandler(tool.Name))
	}

	// Register resources
	resources := DefineMCPResources()
	for _, resource := range resources {
		mcpServer.AddResource(resource, s.createResourceHandler(resource.URI))
	}

	s.mcpServer = mcpServer

	cfg.Logger.Info().
		Int("tool_count", len(tools)).
		Int("resource_count", len(resources)).
		Msg("MCP server initialized")

	return s, nil
}

// createToolHandler creates a handler for a specific tool
func (s *Server) createToolHandler(toolName string) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		var status string
		var err error

		defer func() {
			duration := time.Since(start).Seconds()
			s.metrics.MCPRequestDuration.WithLabelValues(toolName, status).Observe(duration)
			s.metrics.MCPRequestTotal.WithLabelValues(toolName, status).Inc()

			if err != nil {
				errorCode := fmt.Sprintf("%d", apperrors.GetErrorCode(err))
				s.metrics.MCPRequestErrors.WithLabelValues(toolName, errorCode).Inc()
			}
		}()

		// Parse arguments. As of mcp-go v1, Params.Arguments is `any`;
		// GetArguments asserts it back to a map and yields nil otherwise.
		args := request.GetArguments()
		if args == nil {
			args = make(map[string]interface{})
		}

		// Route to appropriate handler
		var result interface{}
		switch toolName {
		case "search_emails":
			result, err = s.handleSearchEmails(ctx, args)
		case "get_calendar_events":
			result, err = s.handleGetCalendarEvents(ctx, args)
		case "list_recent_files":
			result, err = s.handleListRecentFiles(ctx, args)
		case "list_chats":
			result, err = s.handleListChats(ctx, args)
		case "get_chat_messages":
			result, err = s.handleGetChatMessages(ctx, args)
		case "search_users":
			result, err = s.handleSearchUsers(ctx, args)
		case "get_meeting_transcript":
			result, err = s.handleGetMeetingTranscript(ctx, args)
		case "schedule_meeting":
			result, err = s.handleScheduleMeeting(ctx, args)
		case "search_sharepoint":
			result, err = s.handleSearchSharepoint(ctx, args)
		case "send_teams_message":
			result, err = s.handleSendTeamsMessage(ctx, args)
		case "get_user_org_chart":
			result, err = s.handleGetUserOrgChart(ctx, args)
		case "find_experts":
			result, err = s.handleFindExperts(ctx, args)
		case "extract_file_content":
			result, err = s.handleExtractFileContent(ctx, args)
		case "list_sharepoint_drives":
			result, err = s.handleListSharePointDrives(ctx, args)
		case "get_email_details":
			result, err = s.handleGetEmailDetails(ctx, args)
		case "read_email":
			result, err = s.handleGetEmailDetails(ctx, args)
		case "download_attachment":
			result, err = s.handleDownloadAttachment(ctx, args)
		case "send_email":
			result, err = s.handleSendEmail(ctx, args)
		case "get_sharepoint_page_content":
			result, err = s.handleGetSharePointPageContent(ctx, args)
		case "get_user_availability":
			result, err = s.handleGetUserAvailability(ctx, args)
		case "upload_file":
			result, err = s.handleUploadFile(ctx, args)
		default:
			status = "error"
			err = fmt.Errorf("unknown tool: %s", toolName)
			return nil, err
		}

		if err != nil {
			status = "error"
			return nil, err
		}

		status = "success"

		// Convert result to JSON string
		resultJSON, err := json.Marshal(result)
		if err != nil {
			status = "error"
			return nil, fmt.Errorf("failed to marshal result: %w", err)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.TextContent{
					Type: "text",
					Text: string(resultJSON),
				},
			},
		}, nil
	}
}

// createResourceHandler creates a handler for a specific resource
func (s *Server) createResourceHandler(uri string) server.ResourceHandlerFunc {
	return func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		s.logger.Info().
			Str("uri", request.Params.URI).
			Msg("Reading resource")

		// Handle resource read
		contents, err := s.handleResourceRead(ctx, request.Params.URI)
		if err != nil {
			return nil, err
		}

		return []mcp.ResourceContents{contents}, nil
	}
}

// ServeHTTP handles HTTP requests to the MCP server
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Read the request body first so we can inspect the JSON-RPC method before
	// deciding whether authentication is required.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to read request body")
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var message json.RawMessage = body
	method := extractMCPMethod(body)

	var ctx context.Context
	var userEmail string

	// MCP discovery endpoints do not require authentication.
	if isMCPDiscoveryMethod(method) {
		ctx = observability.WithUserID(r.Context(), "anonymous")
		ctx = observability.WithCorrelationID(ctx, generateCorrelationID())
		s.logger.Info().
			Str("mcp_method", method).
			Msg("Handling unauthenticated MCP discovery request")
	} else {
		// Extract token from Authorization header
		token := extractToken(r)
		if token == "" {
			s.logger.Warn().Msg("Missing authorization token")
			http.Error(w, "Missing authorization token", http.StatusUnauthorized)
			return
		}

		// Skip token validation if disabled (for testing only!)
		if s.disableAuth {
			s.logger.Warn().Msg("⚠️  Token validation disabled - passing token through to MS Graph")
			// Skip validation but use the real token for MS Graph API
			ctx = observability.WithUserID(r.Context(), "test-user")
			ctx = observability.WithCorrelationID(ctx, generateCorrelationID())
			ctx = context.WithValue(ctx, TokenKey, token) // Pass real token to MS Graph!
			userEmail = "test@example.com"
		} else if s.skipTokenValidation {
			// Skip local JWT verification; token will be validated by MS Graph during OBO
			s.logger.Info().Msg("Token signature verification skipped - OBO will validate token")
			claims, err := s.tokenValidator.ParseClaimsWithoutVerification(token)
			if err != nil {
				s.logger.Warn().Err(err).Msg("Could not parse token claims for context; using placeholder")
				ctx = observability.WithUserID(r.Context(), "unknown")
				userEmail = ""
			} else {
				ctx = observability.WithUserID(r.Context(), claims.GetUserID())
				userEmail = claims.Email
			}
			ctx = observability.WithCorrelationID(ctx, generateCorrelationID())
			ctx = context.WithValue(ctx, TokenKey, token)
		} else {
			// Validate token
			claims, err := s.tokenValidator.ValidateToken(r.Context(), token)
			if err != nil {
				s.logger.Error().Err(err).Msg("Token validation failed")
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			// Add user context
			ctx = observability.WithUserID(r.Context(), claims.GetUserID())
			ctx = observability.WithCorrelationID(ctx, generateCorrelationID())
			ctx = context.WithValue(ctx, TokenKey, token)
			userEmail = claims.Email
		}
	}

	// Create new request with updated context
	r = r.WithContext(ctx)

	// Log request
	logger := observability.LoggerFromContext(ctx, *s.logger)
	logger.Info().
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("mcp_method", method).
		Str("user_email", userEmail).
		Msg("MCP request received")

	logger.Info().RawJSON("mcp_request", message).Msg("MCP request body")

	// Handle the message
	response := s.mcpServer.HandleMessage(ctx, message)

	// Log the response
	if respJSON, err := json.Marshal(response); err == nil {
		logger.Info().RawJSON("mcp_response", respJSON).Msg("MCP response body")
	}

	// Write response
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error().Err(err).Msg("Failed to encode response")
	}
}

// getGraphClient retrieves or creates a Graph client from context
func (s *Server) getGraphClient(ctx context.Context) (*msgraph.Client, error) {
	// Get token from context
	incomingToken, ok := ctx.Value(TokenKey).(string)
	if !ok || incomingToken == "" {
		return nil, apperrors.NewTokenValidationError(fmt.Errorf("token not found in context"))
	}

	var graphToken string

	if s.disableAuth {
		// Dev mode: still need OBO exchange to get a Graph-scoped token
		s.logger.Info().Msg("Dev mode: performing OBO exchange for Graph token")
		var err error
		graphToken, err = s.oboExchanger.ExchangeForGraphToken(ctx, incomingToken)
		if err != nil {
			s.logger.Error().Err(err).Msg("Dev mode: OBO exchange failed")
			return nil, fmt.Errorf("failed to exchange token for Graph access (dev mode): %w", err)
		}
		s.logger.Info().Int("graph_token_len", len(graphToken)).Msg("Dev mode: OBO exchange succeeded")
	} else if s.skipTokenValidation {
		// When skipTokenValidation is on, check if caller sent a Graph token (wrong audience for OBO).
		// If so, use it directly so the app works without changing the client; Graph calls use this token as-is (app context).
		claims, err := s.tokenValidator.ParseClaimsWithoutVerification(incomingToken)
		if err == nil && isGraphAudience(claims.Audience) {
			s.logger.Info().Str("aud", claims.Audience).Msg("Using incoming Graph token directly (caller sent Graph token; no OBO)")
			graphToken = incomingToken
		} else {
			var err error
			graphToken, err = s.oboExchanger.ExchangeForGraphToken(ctx, incomingToken)
			if err != nil {
				return nil, fmt.Errorf("failed to exchange token for Graph access: %w", err)
			}
		}
	} else {
		// Production: exchange incoming MCP app token for a Graph token via OBO
		var err error
		graphToken, err = s.oboExchanger.ExchangeForGraphToken(ctx, incomingToken)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange token for Graph access: %w", err)
		}
	}

	// Create Graph client with the token
	cfg := s.graphClientConfig
	cfg.Token = graphToken

	client, err := msgraph.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Graph client: %w", err)
	}

	return client, nil
}

// isGraphAudience returns true if the token audience is MS Graph (so it can be used as Graph token directly).
func isGraphAudience(aud string) bool {
	return aud == "https://graph.microsoft.com" || aud == "00000003-0000-0000-c000-000000000000"
}

// mcpMethodPayload extracts the JSON-RPC method from an MCP request body.
type mcpMethodPayload struct {
	Method string `json:"method"`
}

// extractMCPMethod parses the JSON-RPC method from a raw request body.
// It returns an empty string if the body is not valid JSON or has no method.
func extractMCPMethod(body []byte) string {
	var payload mcpMethodPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.Method
}

// isMCPDiscoveryMethod returns true for MCP methods that do not require
// authentication, such as initialization and capability discovery.
func isMCPDiscoveryMethod(method string) bool {
	switch method {
	case "initialize",
		"notifications/initialized",
		"tools/list",
		"resources/list",
		"prompts/list":
		return true
	default:
		return false
	}
}

// extractToken extracts the bearer token from the Authorization header
func extractToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	// Expected format: "Bearer <token>"
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
		return ""
	}

	return parts[1]
}

// generateCorrelationID generates a unique correlation ID for request tracing
func generateCorrelationID() string {
	// Simple implementation using timestamp
	// In production, use UUID or similar
	return fmt.Sprintf("req_%d", time.Now().UnixNano())
}
