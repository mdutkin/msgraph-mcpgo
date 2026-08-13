package docparse

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestHTMLToMarkdown_Basic(t *testing.T) {
	input := "<h1>Title</h1><p>This is <strong>bold</strong> and <em>italic</em>.</p>"
	result, err := HTMLToMarkdown(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "# Title") {
		t.Errorf("expected heading in result, got:\n%s", result)
	}
	if !strings.Contains(result, "**bold**") {
		t.Errorf("expected bold formatting in result, got:\n%s", result)
	}
	if !strings.Contains(result, "*italic*") {
		t.Errorf("expected italic formatting in result, got:\n%s", result)
	}
}

func TestHTMLToMarkdown_Table(t *testing.T) {
	input := `<table>
		<tr><th>Name</th><th>Value</th></tr>
		<tr><td>Alpha</td><td>100</td></tr>
	</table>`

	result, err := HTMLToMarkdown(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Name") || !strings.Contains(result, "Value") {
		t.Errorf("expected table header cells in result, got:\n%s", result)
	}
	if !strings.Contains(result, "|-------|") {
		t.Errorf("expected table separator in result, got:\n%s", result)
	}
	if !strings.Contains(result, "Alpha") || !strings.Contains(result, "100") {
		t.Errorf("expected table row cells in result, got:\n%s", result)
	}
}

func TestHTMLToMarkdown_List(t *testing.T) {
	input := "<ul><li>First</li><li>Second</li></ul>"

	result, err := HTMLToMarkdown(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "- First") {
		t.Errorf("expected first list item in result, got:\n%s", result)
	}
	if !strings.Contains(result, "- Second") {
		t.Errorf("expected second list item in result, got:\n%s", result)
	}
}

func TestParse_Csv(t *testing.T) {
	csv := "Name,Value,Notes\nAlpha,100,First Note\nBeta,200,\n"

	result, err := Parse([]byte(csv), "data.csv")
	if err != nil {
		t.Fatalf("unexpected error parsing csv: %v", err)
	}

	expectedRows := []string{
		"| Name | Value | Notes |",
		"| --- | --- | --- |",
		"| Alpha | 100 | First Note |",
		"| Beta | 200 |  |",
	}

	for _, expected := range expectedRows {
		if !strings.Contains(result, expected) {
			t.Errorf("expected row %q in result, but got:\n%s", expected, result)
		}
	}
}

func TestParse_CsvWithBOM(t *testing.T) {
	csv := "\xef\xbb\xbfName,Value\nAlpha,100\n"

	result, err := Parse([]byte(csv), "data.csv")
	if err != nil {
		t.Fatalf("unexpected error parsing csv with BOM: %v", err)
	}

	if !strings.Contains(result, "| Name | Value |") {
		t.Errorf("expected table header in result, got:\n%s", result)
	}
}

func TestParse_Html(t *testing.T) {
	input := "<h2>Section</h2><p>Hello World</p>"

	result, err := Parse([]byte(input), "page.html")
	if err != nil {
		t.Fatalf("unexpected error parsing html: %v", err)
	}

	if !strings.Contains(result, "## Section") {
		t.Errorf("expected heading in result, got:\n%s", result)
	}
	if !strings.Contains(result, "Hello World") {
		t.Errorf("expected paragraph text in result, got:\n%s", result)
	}
}

func TestParse_DocxFallsBackForUnsupportedEmphasis(t *testing.T) {
	data := makeDOCX(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:rPr><w:em w:val="unsupported"/></w:rPr><w:t>Recovered text</w:t></w:r></w:p></w:body></w:document>`)

	result, err := Parse(data, "document.docx")
	if err != nil {
		t.Fatalf("unexpected error parsing DOCX with unsupported emphasis: %v", err)
	}
	if !strings.Contains(result, "Recovered text") {
		t.Fatalf("expected fallback converter to extract document text, got:\n%s", result)
	}
}

func TestParse_UnsupportedFormat(t *testing.T) {
	_, err := Parse([]byte("data"), "file.xyz")
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported file format") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConvertWithTempFile(t *testing.T) {
	converter := func(path string) (string, error) {
		return "converted: " + path, nil
	}

	result, err := convertWithTempFile([]byte("test data"), "document.pdf", converter)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(result, "converted: ") {
		t.Errorf("expected converted prefix, got: %s", result)
	}
}

func makeDOCX(t *testing.T, documentXML string) []byte {
	t.Helper()

	files := map[string]string{
		"[Content_Types].xml":          `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":                  `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":            documentXML,
		"word/_rels/document.xml.rels": `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>`,
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create DOCX entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write DOCX entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close DOCX archive: %v", err)
	}
	return buf.Bytes()
}
