# Email attachment markdown extraction

## Problem

`read_email` / `get_email_details` was returning huge base64 payloads for
attachments (148K tokens across 5 emails), exceeding LLM context budgets.
PNGs especially added bulk without value — image bytes are not interpretable
by the LLM as text.

## Decision

For every file attachment on an email:

- **Text-extractable formats** (PDF, DOCX, XLSX, PPTX, HTML, CSV, TXT, MD,
  JSON, XML, YAML, RTF, ODT, code files): extract to markdown via the
  existing `internal/docparse` package, truncate to 75 KB, return under
  `contentMarkdown`. No LLM call.
- **Binary formats** (images, audio, video, archives, executables, unknown
  `application/octet-stream`): metadata only, with a `note` directing the
  caller to `download_attachment`.
- **Oversize** (raw bytes > 4 MB, Graph's inline cap): metadata + note.
  Avoids extra `$value` Graph fetch.
- **Extraction failures** (malformed PDF etc.): metadata + note. Never
  fail the whole email fetch.

Also: truncate the email `body` to 75 KB chars with a marker.

## Implementation

### New package `internal/attachments`

- `Extractor{MaxBytes, MaxMarkdownLen}` with sensible defaults (4 MB, 75 KB).
- `Classify(filename, contentType)` → `KindText | KindBinary | KindUnsupported`
  using an extension allow-list and a MIME-type fallback.
- `Extract(ctx, data, filename, contentType)` → `(markdown, note, kind)`.
  Never returns an error; failures end up in `note`.
- Table-driven tests for classification + extraction + truncation.

### `internal/msgraph/emails.go`

- Add `ContentMarkdown string` on `EmailDetailsAttachment`.
- Add `AttachmentExtractor *attachments.Extractor` and `MaxBodyChars int`
  on `GetEmailDetailsRequest`.
- Update `parseEmailAttachments` and `convertToEmailDetails` to use the
  extractor when set. When the extractor is wired, `ContentBase64` is no
  longer populated for any attachment (it remains available via
  `download_attachment`).

### Wiring

- `mcp.ServerConfig.AttachmentExtractor` and `Server.attachmentExtractor`.
- `handleGetEmailDetails` passes the extractor into
  `GetEmailDetailsRequest{...}`.
- `cmd/server/main.go` constructs the extractor at startup.
- Tool descriptions in `internal/mcp/tools.go` updated to explain the new
  behavior.

### Observability

Two new Prometheus metrics in `internal/observability/metrics.go`:

- `attachment_extraction_total{kind, status}` — counter
- `attachment_extraction_latency_seconds{kind, status}` — histogram

`kind` is `text | binary | oversize | unsupported | failed`; `status` is
`success | skipped | failed`.

## Out of scope

- LLM-based summarization (rejected).
- Caching extracted markdown across calls (cheap to add later).
- Modifying `download_attachment` (already returns raw bytes; remains the
  escape hatch for binary content).
- Modifying `SendEmailRequest.Attachments` (outbound, separate concern).
