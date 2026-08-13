package attachments

import (
	"context"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		want        Kind
	}{
		// Text by extension.
		{"pdf", "report.pdf", "", KindText},
		{"docx", "notes.docx", "", KindText},
		{"xlsx uppercase ext", "Sheet1.XLSX", "", KindText},
		{"csv", "data.csv", "application/octet-stream", KindText},
		{"txt", "log.txt", "", KindText},
		{"html", "page.html", "", KindText},
		{"json", "config.json", "", KindText},
		{"md", "README.md", "", KindText},

		// Text by MIME type when extension is missing or unknown.
		{"pdf via mime", "document", "application/pdf", KindText},
		{"html via mime", "page", "text/html; charset=utf-8", KindText},
		{"json via mime", "blob", "application/json", KindText},

		// Binary by extension.
		{"png", "logo.png", "", KindBinary},
		{"jpeg", "photo.jpg", "", KindBinary},
		{"jpeg long ext", "photo.jpeg", "", KindBinary},
		{"gif", "anim.gif", "", KindBinary},
		{"svg", "vector.svg", "", KindBinary},
		{"webp", "image.webp", "", KindBinary},
		{"tiff", "scan.tiff", "", KindBinary},
		{"mp4", "video.mp4", "", KindBinary},
		{"mp3", "song.mp3", "", KindBinary},
		{"zip", "archive.zip", "", KindBinary},
		{"exe", "setup.exe", "", KindBinary},

		// Binary by MIME prefix.
		{"image png via mime", "blob", "image/png", KindBinary},
		{"image jpeg via mime", "blob", "image/jpeg", KindBinary},
		{"audio via mime", "blob", "audio/mpeg", KindBinary},
		{"video via mime", "blob", "video/mp4", KindBinary},

		// Binary by MIME type.
		{"octet-stream via mime", "blob", "application/octet-stream", KindBinary},
		{"zip via mime", "blob", "application/zip", KindBinary},
		{"msdownload via mime", "blob", "application/x-msdownload", KindBinary},

		// Extension takes precedence when both ext and content type disagree
		// (e.g. someone sends a .png with a misleading text/plain).
		{"png ext beats text content type", "image.png", "text/plain", KindBinary},

		// Unknown.
		{"no ext no mime", "blob", "", KindUnsupported},
		{"unknown ext unknown mime", "blob.xyz", "application/x-mystery", KindUnsupported},
	}

	e := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.Classify(tt.filename, tt.contentType)
			if got != tt.want {
				t.Errorf("Classify(%q, %q) = %q, want %q",
					tt.filename, tt.contentType, got, tt.want)
			}
		})
	}
}

func TestExtractTextFormats(t *testing.T) {
	tests := []struct {
		name        string
		filename    string
		contentType string
		data        []byte
		wantKind    Kind
		wantNoteSub string
	}{
		{
			name:     "small txt passes through unchanged",
			filename: "note.txt",
			data:     []byte("hello world"),
			wantKind: KindText,
		},
		{
			name:        "html converts to markdown",
			filename:    "page.html",
			contentType: "text/html",
			data:        []byte("<h1>Hi</h1><p>This is <b>bold</b>.</p>"),
			wantKind:    KindText,
		},
		{
			name:     "txt exactly at markdown cap is not truncated",
			filename: "log.txt",
			data:     []byte(strings.Repeat("a", DefaultMaxMarkdownLen)),
			wantKind: KindText,
		},
		{
			name:        "txt above markdown cap is truncated",
			filename:    "log.txt",
			data:        []byte(strings.Repeat("a", DefaultMaxMarkdownLen+500)),
			wantKind:    KindText,
			wantNoteSub: "truncated",
		},
		{
			name:        "txt above byte cap is oversize",
			filename:    "log.txt",
			data:        []byte(strings.Repeat("a", int(DefaultMaxBytes)+1)),
			wantKind:    KindOversize,
			wantNoteSub: "too large",
		},
		{
			name:     "binary png returns no markdown",
			filename: "logo.png",
			data:     []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
			wantKind: KindBinary,
		},
		{
			name:     "unsupported format returns no markdown",
			filename: "blob.xyz",
			data:     []byte("????"),
			wantKind: KindUnsupported,
		},
	}

	e := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, note, kind := e.Extract(context.Background(), tt.data, tt.filename, tt.contentType)
			if kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", kind, tt.wantKind)
			}
			if tt.wantNoteSub != "" && !strings.Contains(note, tt.wantNoteSub) {
				t.Errorf("note %q does not contain %q", note, tt.wantNoteSub)
			}
			if tt.wantKind == KindBinary && md != "" {
				t.Errorf("binary should produce no markdown, got %q", md)
			}
			if tt.wantKind == KindUnsupported && md != "" {
				t.Errorf("unsupported should produce no markdown, got %q", md)
			}
			if tt.wantKind == KindOversize && md != "" {
				t.Errorf("oversize should produce no markdown, got %q", md)
			}
		})
	}
}

func TestExtractCustomLimits(t *testing.T) {
	e := &Extractor{
		MaxBytes:       100,
		MaxMarkdownLen: 50,
	}

	// 200 bytes of text exceeds the 100-byte input cap → oversize.
	_, note, kind := e.Extract(context.Background(), []byte(strings.Repeat("a", 200)), "x.txt", "")
	if kind != KindOversize {
		t.Fatalf("kind = %q, want %q", kind, KindOversize)
	}
	if !strings.Contains(note, "100 B") {
		t.Errorf("note should mention the 100 B cap, got %q", note)
	}

	// 60 bytes of text fits input cap but exceeds 50-byte markdown cap.
	_, note, kind = e.Extract(context.Background(), []byte(strings.Repeat("a", 60)), "x.txt", "")
	if kind != KindText {
		t.Fatalf("kind = %q, want %q", kind, KindText)
	}
	if !strings.Contains(note, "truncated") {
		t.Errorf("note should mention truncation, got %q", note)
	}
}

func TestTruncate(t *testing.T) {
	t.Run("under cap unchanged", func(t *testing.T) {
		s, trunc := truncate("hello", 100)
		if s != "hello" || trunc {
			t.Errorf("got (%q, %v), want (%q, false)", s, trunc, "hello")
		}
	})

	t.Run("at cap unchanged", func(t *testing.T) {
		s, trunc := truncate("hello", 5)
		if s != "hello" || trunc {
			t.Errorf("got (%q, %v), want (%q, false)", s, trunc, "hello")
		}
	})

	t.Run("over cap truncated with marker", func(t *testing.T) {
		original := strings.Repeat("a", 1000)
		s, trunc := truncate(original, 100)
		if !trunc {
			t.Error("expected truncation")
		}
		if len(s) > 100 {
			t.Errorf("len(s)=%d, want <= 100", len(s))
		}
		if !strings.Contains(s, "[truncated by attachment extractor") {
			t.Errorf("expected truncation marker, got %q", s)
		}
		if !strings.Contains(s, "1000") {
			t.Errorf("expected marker to include original size 1000, got %q", s)
		}
	})
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{5 * 1024 * 1024, "5.0 MB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := humanBytes(tt.n); got != tt.want {
				t.Errorf("humanBytes(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}
