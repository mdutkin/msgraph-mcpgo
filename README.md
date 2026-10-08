# msgraph-mcpgo

A Go-based [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) server that gives AI clients delegated access to Microsoft 365 through Microsoft Graph. It supports Outlook, calendars, Teams, OneDrive, SharePoint, directory search and document extraction.

> **Important:** This server operates on behalf of signed-in users. Review the requested Microsoft Graph permissions, apply least privilege, and never expose it without authentication in production.

## Features

- MCP Streamable HTTP transport with tools and resources
- Forwards the caller's Microsoft Graph token; holds no credential of its own
- Verifies an Entra identity assertion against cached tenant signing keys, with rotation handled on demand
- Outlook email search, reading, attachments, and sending
- Calendar events, availability lookup, and Teams meeting scheduling
- Teams chat retrieval and sending
- OneDrive and SharePoint search, upload, page reading, and document extraction
- Directory search, organization charts, and expert discovery
- Per-user rate limiting, retries, circuit breaking, structured logs, health checks, and Prometheus metrics

## Architecture

    MCP client
        |
        | Authorization:        Graph access token   (forwarded)
        | X-Identity-Assertion: Entra ID token       (verified, then discarded)
        v
    msgraph-mcpgo          (holds no credential)
        |                      \
        | the Graph token,      `--> Entra JWKS: verify the assertion's signature
        | forwarded unchanged
        v
    Microsoft Graph        (decides what the token may do)

The server holds no client secret, performs no on-behalf-of exchange and has no
delegated permission of its own, so it cannot act for a user who is not
currently calling it. What a caller may read or write is decided entirely by the
delegated permissions inside their own Graph token, and enforced by Microsoft
Graph. No caller state is held, so any task can serve any request.

### Why there are two credentials

The Graph token cannot be verified here, and that is not a limitation of this
implementation. Microsoft does not publish signing keys for tokens issued to its
own APIs, and documents that a resource must only validate tokens whose audience
is itself:

> APIs and web applications must only validate tokens that have an `aud` claim
> that matches the application. [...] For example, you can't validate tokens for
> Microsoft Graph according to these rules due to their proprietary format.

So the identity is carried by a second credential that *can* be verified: an
Entra token issued for a registered application. Its signature is checked
against the tenant's published keys, and the verified identity is what reaches
the audit log and the rate limiter. The Graph token stays an opaque credential
that is forwarded and never trusted for identity.

Both credentials must describe the same user and the same directory. That
binding is not cryptographic — nothing in the assertion commits to the Graph
token — so a caller could in principle pair a valid assertion with another
user's Graph token. Doing so gains them nothing, because holding that token
already lets them call Microsoft Graph directly without this server. What the
check buys is audit integrity: a Graph call cannot be recorded under an identity
that does not match the credential used to make it.

Set `AUTH_MODE=graph_passthrough` to run with the Graph token alone. Nothing is
then signature-verified and the recorded identity is whatever the token claims,
which is adequate only where the caller is already trusted.

### Signing keys

In `verified_identity` mode the tenant's signing keys are discovered through the
OpenID configuration document and cached. They are re-read on
`JWKS_REFRESH_INTERVAL`, and additionally re-read on demand whenever an
assertion arrives signed by a key the cache has not seen, which is what makes a
key rotation take effect at once instead of at the next interval.

On-demand reads are rate limited by `JWKS_MIN_REFRESH_INTERVAL`, so a caller
sending unrecognised key identifiers cannot turn this service into a request
amplifier against Entra. The scheduled and on-demand budgets are separate: a
startup fetch does not consume the on-demand allowance.

## Requirements

- Go 1.26 or later
- A Microsoft Entra ID tenant and app registration
- Docker with BuildKit (optional)

## Microsoft Entra ID setup

This server needs **no app registration of its own**. The calling application
obtains the Graph token, so the delegated Microsoft Graph permissions are
consented on *that* application's registration.

Add only the permissions your exposed tools need, and withhold the matching
tools in `tools.yaml` for anything you do not consent. The full tool set uses:

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

Some permissions require tenant administrator consent.

The token sent to /mcp must have Microsoft Graph as its audience
(`https://graph.microsoft.com`, or the v1.0 equivalent
`00000003-0000-0000-c000-000000000000`) and must be issued by the tenant named
in `AZURE_TENANT_ID`. A token whose audience is this server, which is what an
on-behalf-of design would receive, is refused with an explanation.

### LibreChat / CorningGPT

LibreChat resolves `{{LIBRECHAT_GRAPH_ACCESS_TOKEN}}` into a Graph token for the
signed-in user and `{{LIBRECHAT_OPENID_ID_TOKEN}}` into that user's Entra ID
token, so the client configuration is:

    mcpServers:
      msgraph:
        type: streamable-http
        url: "https://msgraph-mcp.example.com/mcp"
        headers:
          Authorization: "Bearer {{LIBRECHAT_GRAPH_ACCESS_TOKEN}}"
          X-Identity-Assertion: "{{LIBRECHAT_OPENID_ID_TOKEN}}"

Set `IDENTITY_AUDIENCES` to the LibreChat application's client ID, which is the
`aud` of that ID token. LibreChat refuses to resolve an expired ID token, so an
assertion that reaches this server is one LibreChat considered current.

To have the assertion issued for *this* server instead, register it as an API in
Entra, have the client request that scope, and put its identifier — for example
`api://<client-id>` — in `IDENTITY_AUDIENCES`. Verification is identical; only
which registration mints the assertion differs.

LibreChat requests `https://graph.microsoft.com/.default`, which returns only
the permissions already consented on the LibreChat app registration. A tool that
needs a permission missing from that registration will fail with a Graph
authorization error, not with a 401 from this server.

## Quick start

Clone and configure the project:

    git clone https://github.com/fnfbraga/msgraph-mcpgo.git
    cd msgraph-mcpgo
    cp .env.example .env

Set at least this value in .env:

    AZURE_TENANT_ID=your-tenant-id

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

## Container image

    make docker-build
    make docker-run

The image runs as uid 10001, needs no privileged port and writes nothing to
disk, so it should be deployed with a read-only root filesystem and all
capabilities dropped. `make docker-run` applies both locally.

Base images are pinned to a patch version, and the build stamps the version and
commit into the binary through `-ldflags`; both are reported in the startup log
so a running task can be tied back to a commit without inspecting the image.

Behind a TLS-intercepting proxy, pass the corporate root CA as a BuildKit
secret so that module download and runtime egress trust it:

    make docker-build CACERTS=/path/to/corporate-root-ca.pem

The certificate is mounted as a secret rather than copied into the build
context, so it never becomes an image layer and never reaches a registry. The
secret is optional and the same Dockerfile builds unchanged without it.

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| AZURE_TENANT_ID | Yes | — | Microsoft Entra tenant ID. A token from another directory is refused |
| AUTH_MODE | No | graph_passthrough | `verified_identity`, `graph_passthrough` or `disabled`. See Architecture |
| IDENTITY_AUDIENCES | When verifying | — | Comma-separated accepted `aud` values of the assertion, one per application allowed to vouch for a caller |
| IDENTITY_ASSERTION_HEADER | No | X-Identity-Assertion | Header carrying the verifiable Entra token |
| JWKS_REFRESH_INTERVAL | No | 12h | How often the tenant's signing keys are re-read |
| JWKS_MIN_REFRESH_INTERVAL | No | 5m | Floor between on-demand key reads triggered by an unknown key identifier |
| ENTRA_DISCOVERY_URL | No | — | Overrides the OpenID configuration URL. Needed only for a sovereign cloud |
| ENVIRONMENT | No | production | Runtime environment; development enables development logging |
| SERVER_PORT | No | 8080 | HTTP server port |
| PUBLIC_URL | No | http://localhost:8080 | Base URL clients reach. Published as the OAuth protected resource identifier and in every 401 challenge |
| NETWORK_EXPOSURE | No | internet | `internet` requires PUBLIC_URL to be https. `internal` relaxes that for a service reachable only inside the private network, such as one addressed over ECS Service Connect |
| TOOL_POLICY_FILE | No | tools.yaml | YAML document selecting which tools and resources are exposed. When set, a missing file stops startup |
| MCP_STATELESS | No | true | Keep the Streamable HTTP transport free of per-session state. Required when more than one task serves a load balancer target group |
| METRICS_PORT | No | 9090 | Prometheus metrics port |
| GRAPH_TIMEOUT | No | 60s | Microsoft Graph operation timeout |
| MAX_REQUEST_BYTES | No | 33554432 | Maximum MCP request body. 32 MiB admits an upload_file call carrying roughly 24 MiB after base64 expansion |
| READ_HEADER_TIMEOUT | No | 10s | Deadline for a client to send request headers |
| READ_TIMEOUT | No | 30s | Deadline for the whole request read |
| WRITE_TIMEOUT | No | 120s | Deadline for the response write. Must exceed GRAPH_TIMEOUT |
| IDLE_TIMEOUT | No | 120s | Keep-alive idle deadline |
| LOG_LEVEL | No | info | Structured logging level |
| RATE_LIMIT_PER_USER | No | 100 | Sustained requests per minute per user, per task. 0 disables throttling |
| RATE_LIMIT_BURST | No | 0 | Back-to-back requests allowed before the sustained rate applies. 0 selects a quarter of RATE_LIMIT_PER_USER, floor 5 |
| RATE_LIMIT_IDLE_TTL | No | 15m | Retention for an unused per-user bucket |


`AUTH_MODE=disabled` is refused at startup unless ENVIRONMENT names a
development environment (`development` or `dev`). The
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

## Exposing a subset of the tools

The binary implements every tool listed below, but a deployment decides which
of them to expose. Copy `tools.example.yaml` to `tools.yaml`, or point
`TOOL_POLICY_FILE` at a path:

    tools:
      default: all
      exclude:
        - search_emails
        - read_email
        - send_email

    resources:
      default: all
      exclude:
        - msgraph://emails

Two shapes are accepted, and mixing them is an error:

| Shape | Meaning |
| --- | --- |
| `default: all` with `exclude` | Expose everything except the listed entries |
| `default: none` with `include` | Expose only the listed entries |

Omitting `default` means `all`, so a file that lists only exclusions reads the
way it behaves.

A withheld tool is never registered with the MCP server. It is absent from
`tools/list`, and a call naming it is answered by the protocol layer with
`tool 'search_emails' not found`. There is no filter for a caller to bypass.

Every name in the file is checked against the tools the binary implements, and
an unrecognised name stops startup. This matters: a typo such as
`search_email` for `search_emails` would otherwise leave mailbox search
exposed while the file states it is withheld. Misspelled keys are rejected for
the same reason.

Withhold the matching resource whenever you withhold a tool. The three
resources expose recent mail, recent files and upcoming events through a
separate interface, so excluding the mail tools alone still leaves mailbox
content reachable at `msgraph://emails`.

When `TOOL_POLICY_FILE` is unset and `tools.yaml` is absent, everything is
exposed. Set the variable explicitly in a deployed environment so that a
missing file fails instead of silently exposing the full surface.

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
