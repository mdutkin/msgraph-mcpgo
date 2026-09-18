# msgraph-mcpgo

A Go-based [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that gives AI clients delegated access to Microsoft 365 through Microsoft Graph. It supports Outlook, calendars, Teams, OneDrive, SharePoint, directory search and document extraction.

> **Important:** This server operates on behalf of signed-in users. Review the requested Microsoft Graph permissions, apply least privilege, and never expose it without authentication in production.

## Features

- HTTP MCP endpoint with tools and resources
- Microsoft Entra ID token validation and OAuth On-Behalf-Of exchange
- Outlook email search, reading, attachments, and sending
- Calendar events, availability lookup, and Teams meeting scheduling
- Teams chat retrieval and sending
- OneDrive and SharePoint search, upload, page reading, and document extraction
- Directory search, organization charts, and expert discovery
- Retries, circuit breaking, structured logs, health checks, and Prometheus metrics

## Architecture

    MCP client
        |
        | Bearer token + JSON-RPC
        v
    msgraph-mcpgo
        |
        | Entra validation + OAuth OBO
        v
    Microsoft Graph

The client sends a Microsoft Entra ID access token to POST /mcp. The server validates it, exchanges it through the OBO flow for a delegated Microsoft Graph token, and invokes Graph APIs as that user.

## Requirements

- Go 1.26 or later
- A Microsoft Entra ID tenant and app registration
- Docker (optional)

## Microsoft Entra ID setup

Create an app registration and configure it for the access-token flow used by your MCP client. Add only the delegated Microsoft Graph permissions required by your deployment.

The current OBO implementation requests:

- User.Read
- User.Read.All
- Mail.Read
- Mail.Send
- Calendars.Read
- Calendars.ReadWrite
- Files.Read.All
- Files.ReadWrite
- Chat.Read
- ChatMessage.Send
- OnlineMeetings.Read
- OnlineMeetingTranscript.Read.All
- Sites.Read.All
- offline_access

Some permissions require tenant administrator consent. The token sent to /mcp must be issued by the configured tenant and use AZURE_CLIENT_ID as its expected audience. The app registration also needs a client secret for OBO exchange.

## Quick start

Clone and configure the project:

    git clone https://github.com/fnfbraga/msgraph-mcpgo.git
    cd msgraph-mcpgo
    cp .env.example .env

Set at least these values in .env:

    AZURE_TENANT_ID=your-tenant-id
    AZURE_CLIENT_ID=your-client-id
    AZURE_CLIENT_SECRET=your-client-secret

AZURE_CLIENT_SECRET is required by the application even though it is not currently listed in .env.example.

Run the server:

    make run

Or build and run the binary:

    make build
    ./bin/msgraph-mcp-server

Default endpoints:

- MCP: http://localhost:8080/mcp
- Browser test console: http://localhost:8080/test
- Liveness: http://localhost:8080/health/live
- Readiness: http://localhost:8080/health/ready
- Prometheus metrics: http://localhost:9090/metrics

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| AZURE_TENANT_ID | Yes | — | Microsoft Entra tenant ID |
| AZURE_CLIENT_ID | Yes | — | App registration client ID and expected token audience |
| AZURE_CLIENT_SECRET | Yes | — | App secret used for OBO exchange |
| ENVIRONMENT | No | production | Runtime environment; development enables development logging |
| SERVER_PORT | No | 8080 | HTTP server port |
| METRICS_PORT | No | 9090 | Prometheus metrics port |
| GRAPH_TIMEOUT | No | 60s | Microsoft Graph operation timeout |
| TOKEN_CACHE_TTL | No | 5m | Token cache lifetime |
| JWKS_CACHE_TTL | No | 24h | Entra JWKS cache lifetime |
| LOG_LEVEL | No | info | Structured logging level |
| RATE_LIMIT_PER_USER | No | 100 | Configured per-user request limit |
| DISABLE_AUTH | No | false | Development-only authentication bypass |
| SKIP_TOKEN_VALIDATION | No | false | Skip local JWT verification; Graph still validates during OBO |

> **Warning:** Never enable DISABLE_AUTH in a public or production deployment. Use SKIP_TOKEN_VALIDATION only when local JWKS verification cannot be used and you understand the reduced defense in depth.

## Using the MCP endpoint

Discovery methods can be called without authentication. Tool and resource requests require a bearer token unless authentication is explicitly disabled.

List tools:

    curl -X POST http://localhost:8080/mcp \
      -H "Content-Type: application/json" \
      -d "{"jsonrpc":"2.0","id":1,"method":"tools/list"}"

Call an authenticated tool:

    curl -X POST http://localhost:8080/mcp \
      -H "Authorization: Bearer YOUR_ENTRA_ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -d "{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_emails","arguments":{"query":"subject:project","top":10}}}"

The browser console at /test supports the Entra device-code flow through /auth/devicecode and /auth/token.

## MCP tools

### Mail

- search_emails — Search and filter email metadata
- get_email_details / read_email — Read a message and extract supported attachments
- download_attachment — Return raw attachment content as base64
- send_email — Send mail as the authenticated user

### Calendar and meetings

- get_calendar_events — Retrieve events for a time range
- get_user_availability — Check free/busy status without exposing meeting details
- schedule_meeting — Create a Teams meeting
- get_meeting_transcript — Retrieve transcripts for meetings organized by the user

### Teams

- list_chats — List chats and participants
- get_chat_messages — Retrieve messages and supported attachment content
- send_teams_message — Send a chat message

### OneDrive and SharePoint

- list_recent_files — List recently modified OneDrive files
- search_sharepoint — Search SharePoint and OneDrive with KQL
- extract_file_content — Convert supported documents to structured Markdown
- list_sharepoint_drives — List document libraries for a site
- get_sharepoint_page_content — Extract content from SharePoint .aspx pages
- upload_file — Upload up to 4 MB of base64-encoded content to OneDrive

### Users

- search_users — Search the tenant directory
- get_user_org_chart — Retrieve a manager and direct reports
- find_experts — Find colleagues by skills or project context

Detailed schemas, arguments, and limits are available through tools/list.

## MCP resources

| URI | Description |
| --- | --- |
| msgraph://emails | Recent emails |
| msgraph://files | Recently modified OneDrive files |
| msgraph://calendar | Upcoming calendar events |

## Document extraction

The extraction pipeline supports common Office formats, PDF, HTML, CSV, plain text, EPUB, ZIP, Jupyter notebooks, and feeds. Extracted content is returned as Markdown.

To keep responses within practical context limits:

- Extracted text is truncated at 75 KB
- Binary and unsupported formats return metadata instead of inline content
- Attachments larger than 4 MB return metadata only
- Source file download limits are documented in each tool schema

Treat extracted document and message content as sensitive user data.

## Docker

Build and run with the Makefile:

    make docker-build
    make docker-run

Or use Docker directly:

    docker build -t msgraph-mcpgo:latest .
    docker run --rm --env-file .env -p 8080:8080 -p 9090:9090 msgraph-mcpgo:latest

The image builds a statically linked Linux amd64 binary and runs it on Alpine Linux.

## Development

    make help          # List commands
    make test          # Race detection and coverage
    make test-short    # Short test suite
    make vet           # Go static analysis
    make fmt           # Format Go code
    make lint          # golangci-lint
    make dev           # Hot reload with Air

Standard validation:

    go test -race ./...
    go build ./...
    go vet ./...

## Security and privacy

- Keep .env, client secrets, access tokens, and API keys out of version control.
- Terminate TLS at the service or a trusted reverse proxy in production.
- Restrict access to the test console and metrics endpoint as appropriate.
- Use least-privilege Graph permissions and review tenant consent regularly.
- Do not log tokens or message and document contents when adding handlers.

## Contributing

Issues and pull requests are welcome. Before submitting changes, run:

    go test -race ./...
    go build ./...
    go vet ./...

## License

Licensed under the [Zero-Clause BSD License](LICENSE). You may use, copy, modify, and distribute this software for any purpose, with or without fee.
