package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	stateless           bool
	endpointPath        string
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

	// Stateless disables per-session transport state. Keep it true for any
	// deployment with more than one task behind a load balancer.
	Stateless bool

	// EndpointPath is the path the transport is mounted on. It must match the
	// mux route so that the transport builds correct absolute URLs.
	EndpointPath string
}

// DefaultEndpointPath is the path the MCP transport is mounted on.
const DefaultEndpointPath = "/mcp"

// NewServer creates a new MCP server
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.EndpointPath == "" {
		cfg.EndpointPath = DefaultEndpointPath
	}

	s := &Server{
		tokenValidator:      cfg.TokenValidator,
		oboExchanger:        cfg.OBOExchanger,
		attachmentExtractor: cfg.AttachmentExtractor,
		circuitBreaker:      cfg.CircuitBreaker,
		logger:              cfg.Logger,
		metrics:             cfg.Metrics,
		disableAuth:         cfg.DisableAuth,
		skipTokenValidation: cfg.SkipTokenValidation,
		stateless:           cfg.Stateless,
		endpointPath:        cfg.EndpointPath,
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

// Handler returns the MCP Streamable HTTP transport for this server.
//
// The transport is the one defined by the MCP specification: a single endpoint
// that accepts JSON-RPC over POST and can upgrade a response to an SSE stream
// when a tool reports progress. It replaces a hand-rolled POST-only handler
// that implemented no part of the transport contract, which left the server
// unable to stream, unable to express a session, and unable to answer the
// GET and DELETE verbs a compliant client issues.
//
// Statelessness is deliberate for the default deployment. In stateful mode the
// transport keeps per-session state in task memory, so a second request from
// the same client must reach the same task. Behind an Application Load
// Balancer spreading traffic over an autoscaling Fargate service that does not
// hold, and the client would see its session disappear. Stateless mode makes
// every request self-contained, which is what lets the service scale
// horizontally without sticky routing.
func (s *Server) Handler(opts ...server.StreamableHTTPOption) http.Handler {
	base := []server.StreamableHTTPOption{
		server.WithStateLess(s.stateless),
		server.WithEndpointPath(s.endpointPath),
	}
	return server.NewStreamableHTTPServer(s.mcpServer, append(base, opts...)...)
}

// getGraphClient retrieves or creates a Graph client from context
func (s *Server) getGraphClient(ctx context.Context) (*msgraph.Client, error) {
	// The token is placed in the context by the authentication middleware.
	// Its absence means the request bypassed authentication, which is a bug
	// rather than an anonymous caller, so it fails closed.
	incomingToken, ok := auth.TokenFromContext(ctx)
	if !ok {
		return nil, apperrors.NewTokenValidationError(fmt.Errorf("no caller credential in context"))
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
