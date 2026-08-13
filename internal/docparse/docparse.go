// Package docparse extracts text content from documents (Office files, PDFs,
// HTML, and many others) and returns structured markdown suitable for LLM
// consumption.
//
// Parsing is delegated to specialized libraries:
//   - DOCX and PDF      -> github.com/edamplified/go2markdown
//   - HTML                -> github.com/JohannesKaufmann/html-to-markdown/v2
//   - Everything else     -> github.com/conductor-oss/markitdown
package docparse

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"github.com/conductor-oss/markitdown"
	"github.com/edamplified/go2markdown"
)

// htmlConverter is a reusable HTML-to-Markdown converter with table support.
var htmlConverter = converter.NewConverter(
	converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(),
	),
)

// fileConverter converts a file on disk to Markdown.
type fileConverter func(path string) (string, error)

// utf8BOM is the UTF-8 byte order marker.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parse detects the file format from filename and extracts text content as
// structured markdown. Returns an error for unsupported formats.
func Parse(data []byte, filename string) (string, error) {
	// Strip UTF-8 BOM if present; some libraries (e.g. CSV converters) do not
	// handle it, and it is never meaningful for binary Office/PDF formats.
	data = bytes.TrimPrefix(data, utf8BOM)

	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".docx":
		markdown, err := convertWithTempFile(data, filename, go2markdown.ConvertDOCX)
		if err == nil {
			return markdown, nil
		}

		// go2markdown's DOCX reader is intentionally strict and can reject an
		// otherwise readable document because of an unsupported formatting value.
		// markitdown uses an independent, more tolerant OOXML parser.
		fallback, fallbackErr := convertWithMarkitdown(data, filename, ext)
		if fallbackErr != nil {
			return "", fmt.Errorf("primary DOCX conversion failed: %v; fallback conversion failed: %w", err, fallbackErr)
		}
		return fallback, nil
	case ".pdf":
		return convertWithTempFile(data, filename, go2markdown.ConvertPDF)
	case ".html", ".htm":
		return HTMLToMarkdown(string(data))
	default:
		// All other formats are handled by markitdown.
		return convertWithMarkitdown(data, filename, ext)
	}
}

// convertWithMarkitdown converts binary data to Markdown using the
// conductor-oss/markitdown library. It covers formats such as XLSX, XLS,
// PPTX, CSV, DOC, EPUB, ZIP, IPYNB, RSS/Atom, and plain text.
func convertWithMarkitdown(data []byte, filename, ext string) (string, error) {
	md := markitdown.New()
	result, err := md.ConvertReader(bytes.NewReader(data), markitdown.StreamInfo{
		Extension: ext,
		Filename:  filename,
	})
	if err != nil {
		if markitdown.IsUnsupportedFormat(err) {
			return "", fmt.Errorf("unsupported file format: %s", ext)
		}
		return "", fmt.Errorf("failed to convert %s: %w", filename, err)
	}
	return result.Markdown, nil
}

// HTMLToMarkdown converts an HTML string into clean Markdown text.
func HTMLToMarkdown(raw string) (string, error) {
	markdown, err := htmlConverter.ConvertString(raw)
	if err != nil {
		return "", fmt.Errorf("failed to convert HTML to Markdown: %w", err)
	}
	return strings.TrimSpace(markdown), nil
}

// convertWithTempFile writes data to a temporary file and invokes the provided
// file-based converter. The temporary file is cleaned up afterwards.
func convertWithTempFile(data []byte, filename string, convert fileConverter) (string, error) {
	tmpFile, err := os.CreateTemp("", "docparse-*"+filepath.Ext(filename))
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for %s: %w", filename, err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("failed to write temp file for %s: %w", filename, err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("failed to close temp file for %s: %w", filename, err)
	}

	markdown, err := convert(tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("failed to convert %s: %w", filename, err)
	}
	return markdown, nil
}
