# msgraph-mcpgo

A Go-based [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that gives AI clients delegated access to Microsoft 365 through Microsoft Graph. It supports Outlook, calendars, Teams, OneDrive, SharePoint, directory search and document extraction.

> **Important:** This server operates on behalf of signed-in users. Review the requested Microsoft Graph permissions, apply least privilege, and never expose it without authentication in production.

## Features

- MCP Streamable HTTP transport with tools and resources
- Microsoft Entra ID token validation and OAuth On-Behalf-Of exchange
- Outlook email search, reading, attachments, and sending
- Calendar events, availability lookup, and Teams meeting scheduling
- Teams chat retrieval and sending
- OneDrive and SharePoint search, upload, page reading, and document extraction
- Directory search, organization charts, and expert discovery
- Per-user rate limiting, retries, circuit breaking, structured logs, health checks, and Prometheus metrics

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

The client sends a Microsoft Entra ID access token to /mcp over the MCP
Streamable HTTP transport. The server validates it, exchanges it through the OBO
flow for a delegated Microsoft Graph token, and invokes Graph APIs as that user.
No caller state is held on the server, so any task can serve any request.

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
- Protected resource metadata: http://localhost:8080/.well-known/oauth-protected-resource
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
| PUBLIC_URL | No | http://localhost:8080 | Externally reachable base URL. Published as the OAuth protected resource identifier and in every 401 challenge |
| MCP_STATELESS | No | true | Keep the Streamable HTTP transport free of per-session state. Required when more than one task serves a load balancer target group |
| METRICS_PORT | No | 9090 | Prometheus metrics port |
| GRAPH_TIMEOUT | No | 60s | Microsoft Graph operation timeout |
| TOKEN_CACHE_TTL | No | 5m | Token cache lifetime |
| JWKS_CACHE_TTL | No | 24h | Entra JWKS cache lifetime |
| LOG_LEVEL | No | info | Structured logging level |
| RATE_LIMIT_PER_USER | No | 100 | Sustained requests per minute per user, per task. 0 disables throttling |
| RATE_LIMIT_BURST | No | 0 | Back-to-back requests allowed before the sustained rate applies. 0 selects a quarter of RATE_LIMIT_PER_USER, floor 5 |
| RATE_LIMIT_IDLE_TTL | No | 15m | Retention for an unused per-user bucket |
| DISABLE_AUTH | No | false | Development-only authentication bypass |
| SKIP_TOKEN_VALIDATION | No | false | Skip local JWT verification; Graph still validates during OBO |

DISABLE_AUTH and SKIP_TOKEN_VALIDATION are refused at startup unless
ENVIRONMENT names a development environment (`development` or `dev`). The
server exits with a non-zero status and an explanation rather than starting in
a weakened state. Outside development, PUBLIC_URL must also be an absolute
https URL that is not a loopback address, because it is published to clients as
the OAuth protected resource identifier.

ENVIRONMENT itself must be one of `development`, `dev`, `staging`, `production`
or `prod`. An unrecognised value is refused, so a typo cannot produce a
deployment whose posture differs from what the variable says.

## Using the MCP endpoint

Every method requires a bearer token, discovery included. A request without a
valid token receives 401 with a `WWW-Authenticate` challenge that points at the
protected resource metadata document, which is the discovery mechanism defined
by the MCP authorization specification and RFC 9728:

    WWW-Authenticate: Bearer error="invalid_token",
      resource_metadata="https://your-host/.well-known/oauth-protected-resource"

Set `PUBLIC_URL` to the hostname clients actually connect to, otherwise the
advertised metadata URL points at the container.

List tools:

    curl -X POST http://localhost:8080/mcp \
      -H "Authorization: Bearer YOUR_ENTRA_ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json, text/event-stream" \
      -d "{"jsonrpc":"2.0","id":1,"method":"tools/list"}"

Call an authenticated tool:

    curl -X POST http://localhost:8080/mcp \
      -H "Authorization: Bearer YOUR_ENTRA_ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
      -d "{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_emails","arguments":{"query":"subject:project","top":10}}}"

### Development console

A browser console at /test, backed by an Entra device-code proxy at
/auth/devicecode and /auth/token, is available only in a build made with the
`devconsole` tag:

    make run-console

These three routes are unauthenticated by necessity, because their purpose is
to obtain the first token. They are excluded from a normal build by build tag,
so they are absent from the release binary and no misconfiguration can expose
them. Do not build with this tag for any shared or deployed environment: an
open device-code proxy for a confidential client lets a caller start a flow for
this application, persuade an employee to approve the code, and then collect a
delegated token for that employee's mailbox.

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
