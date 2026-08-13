// Package attachments classifies email attachments and extracts the content
// of text-extractable formats as truncated markdown suitable for LLM
// consumption. Binary attachments (images, audio, video, archives,
// executables) are intentionally omitted from the extracted payload to keep
// MCP responses within the LLM context budget.
//
// Extraction is delegated to internal/docparse, which already handles the
// conversion from binary office formats (DOCX, PDF, XLSX, PPTX, …) and HTML
// to markdown. The Extractor here adds the policy layer: format allow-list,
// size cap, and markdown truncation.
//
// This package never returns errors. Classification and extraction failures
// are surfaced through the returned note so the caller can continue to build
// a coherent email response.
package attachments

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fnfbraga/msgraph-mcpgo/internal/docparse"
)

// Kind describes the extraction classification of an attachment.
type Kind string

const (
	// KindText indicates the attachment is text-extractable and was
	// converted to markdown.
	KindText Kind = "text"
	// KindBinary indicates the attachment is a binary format (image, audio,
	// video, archive, executable) whose content is intentionally omitted.
	KindBinary Kind = "binary"
	// KindOversize indicates the attachment is text-extractable in principle
	// but its raw bytes exceed MaxBytes. Caller should use
	// download_attachment.
	KindOversize Kind = "oversize"
	// KindUnsupported indicates the format is not recognised and is not
	// considered safe to inline.
	KindUnsupported Kind = "unsupported"
	// KindFailed indicates classification identified a text format but
	// extraction failed (e.g. malformed PDF).
	KindFailed Kind = "failed"
)

// DefaultMaxBytes matches the Microsoft Graph inline attachment size cap.
// Attachments larger than this would require an additional $value fetch and
// are skipped by default.
const DefaultMaxBytes int64 = 4 * 1024 * 1024

// DefaultMaxMarkdownLen is the truncation length applied to extracted
// markdown. 75 KB ≈ 25K LLM tokens, leaving room for several attachments in
// one email response without blowing the context budget.
const DefaultMaxMarkdownLen = 75 * 1024

// truncationMarker is appended to truncated markdown so the LLM (and
// humans) can see that the document was clipped.
const truncationMarker = "\n\n... [truncated by attachment extractor; original was %d bytes]"

// Extractor classifies attachments and converts text-extractable ones to
// truncated markdown. It is safe for concurrent use.
type Extractor struct {
	// MaxBytes is the maximum raw attachment size the extractor will
	// process. Attachments above this limit are reported as KindOversize.
	// Zero means use DefaultMaxBytes.
	MaxBytes int64
	// MaxMarkdownLen is the maximum number of bytes of extracted markdown
	// returned to the caller. Zero means use DefaultMaxMarkdownLen.
	MaxMarkdownLen int
}

// New returns an Extractor configured with package defaults.
func New() *Extractor {
	return &Extractor{}
}

// maxBytes returns the effective MaxBytes, substituting the default when
// the field is unset.
func (e *Extractor) maxBytes() int64 {
	if e.MaxBytes > 0 {
		return e.MaxBytes
	}
	return DefaultMaxBytes
}

// maxMarkdownLen returns the effective MaxMarkdownLen, substituting the
// default when the field is unset.
func (e *Extractor) maxMarkdownLen() int {
	if e.MaxMarkdownLen > 0 {
		return e.MaxMarkdownLen
	}
	return DefaultMaxMarkdownLen
}

// textExtExtensions lists file extensions that should be extracted as
// markdown. Matching is case-insensitive and includes the leading dot.
var textExtExtensions = map[string]struct{}{
	".pdf":  {},
	".doc":  {},
	".docx": {},
	".xls":  {},
	".xlsx": {},
	".ppt":  {},
	".pptx": {},
	".csv":  {},
	".txt":  {},
	".md":   {},
	".json": {},
	".xml":  {},
	".yaml": {},
	".yml":  {},
	".html": {},
	".htm":  {},
	".rtf":  {},
	".odt":  {},
	".log":  {},
}

// binaryExtExtensions lists file extensions that are never text-extractable
// for the purpose of inline attachment content.
var binaryExtExtensions = map[string]struct{}{
	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".gif":  {},
	".bmp":  {},
	".webp": {},
	".svg":  {},
	".tif":  {},
	".tiff": {},
	".ico":  {},
	".mp3":  {},
	".mp4":  {},
	".mov":  {},
	".wav":  {},
	".ogg":  {},
	".zip":  {},
	".tar":  {},
	".gz":   {},
	".tgz":  {},
	".7z":   {},
	".rar":  {},
	".exe":  {},
	".dll":  {},
	".so":   {},
	".bin":  {},
}

// textMIMETypes maps content types that are unambiguous text formats to a
// canonical lowercase extension. Used when the filename has no usable
// extension.
var textMIMETypes = map[string]string{
	"application/pdf":    "pdf",
	"application/msword": "doc",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
	"application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         "xlsx",
	"application/vnd.ms-powerpoint":                                             "ppt",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": "pptx",
	"application/vnd.oasis.opendocument.text":                                   "odt",
	"application/rtf":    "rtf",
	"text/plain":         "txt",
	"text/csv":           "csv",
	"text/html":          "html",
	"text/xml":           "xml",
	"application/xml":    "xml",
	"text/markdown":      "md",
	"application/json":   "json",
	"application/yaml":   "yaml",
	"text/yaml":          "yaml",
	"application/x-yaml": "yaml",
}

// binaryMIMEPrefixes are MIME prefixes that always mean a binary attachment.
var binaryMIMEPrefixes = []string{
	"image/",
	"audio/",
	"video/",
}

// binaryMIMETypes are specific binary content types that do not match a
// prefix above.
var binaryMIMETypes = map[string]struct{}{
	"application/zip":              {},
	"application/x-zip-compressed": {},
	"application/x-tar":            {},
	"application/gzip":             {},
	"application/x-gzip":           {},
	"application/x-7z-compressed":  {},
	"application/vnd.rar":          {},
	"application/x-rar-compressed": {},
	"application/octet-stream":     {},
	"application/x-msdownload":     {},
	"application/x-executable":     {},
}

// Classify returns the extraction Kind for an attachment based on its
// filename and contentType. The filename is inspected first; the content
// type is used as a fallback when the filename has no usable extension.
// MIME parameters (e.g. "; charset=utf-8") are ignored.
func (e *Extractor) Classify(filename, contentType string) Kind {
	ext := strings.ToLower(filepath.Ext(filename))
	ct := mediaType(contentType)

	if ext != "" {
		if _, ok := textExtExtensions[ext]; ok {
			return KindText
		}
		if _, ok := binaryExtExtensions[ext]; ok {
			return KindBinary
		}
	}

	if ct != "" {
		if _, ok := textMIMETypes[ct]; ok {
			return KindText
		}
		if _, ok := binaryMIMETypes[ct]; ok {
			return KindBinary
		}
		for _, prefix := range binaryMIMEPrefixes {
			if strings.HasPrefix(ct, prefix) {
				return KindBinary
			}
		}
	}

	return KindUnsupported
}

// mediaType returns the lowercased media type of a Content-Type header
// value, with any parameters (e.g. "; charset=utf-8") stripped.
func mediaType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// Extract classifies the attachment and, when appropriate, extracts its
// content as markdown truncated to MaxMarkdownLen. It never returns an
// error; failures and policy decisions are reported via the returned note.
//
// The returned kind is always set so callers can log or branch on it.
func (e *Extractor) Extract(ctx context.Context, data []byte, filename, contentType string) (markdown, note string, kind Kind) {
	kind = e.Classify(filename, contentType)

	switch kind {
	case KindText:
		if int64(len(data)) > e.maxBytes() {
			kind = KindOversize
			return "", noteForOversize(len(data), e.maxBytes()), kind
		}
		md, err := docparse.Parse(data, filename)
		if err != nil {
			kind = KindFailed
			return "", "markdown extraction failed: " + err.Error(), kind
		}
		truncated, wasTruncated := truncate(md, e.maxMarkdownLen())
		if wasTruncated {
			note = fmtTruncationNote(len(md))
		}
		return truncated, note, kind

	case KindBinary:
		return "", "binary attachment omitted to limit LLM context size; use download_attachment with email_id and attachment_id for raw bytes", kind

	case KindUnsupported:
		return "", "unsupported attachment format; use download_attachment with email_id and attachment_id for raw bytes", kind
	}

	return "", "", kind
}

func noteForOversize(size int, cap int64) string {
	return fmt.Sprintf(
		"attachment too large for inline markdown extraction (%s > %s); "+
			"use download_attachment with email_id and attachment_id",
		humanBytes(size), humanBytes(int(cap)),
	)
}

func fmtTruncationNote(originalLen int) string {
	return fmt.Sprintf(
		"extracted markdown truncated at %s (original was %s)",
		humanBytes(DefaultMaxMarkdownLen), humanBytes(originalLen),
	)
}

// truncate returns s clipped to max bytes, with a trailing marker, and a
// boolean indicating whether truncation occurred. The marker is appended
// only when the input was actually truncated.
func truncate(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	marker := fmt.Sprintf(truncationMarker, len(s))
	budget := max
	if budget > len(marker) {
		budget -= len(marker)
	}
	return s[:budget] + marker, true
}

// humanBytes renders a byte count as e.g. "12 KB" or "3.5 MB".
func humanBytes(n int) string {
	const (
		kb = 1024
		mb = 1024 * 1024
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
