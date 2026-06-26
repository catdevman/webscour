package main

import (
	"net/url"
	"strings"
	"testing"
)

func newTestCrawler(out, root string) *crawler {
	return &crawler{cfg: config{outDir: out}, root: root}
}

func TestDestPath(t *testing.T) {
	c := newTestCrawler("downloads", "example.com")
	cases := []struct {
		raw  string
		ext  string
		want string
	}{
		{"https://www.example.com/files/board/2026.pdf", "pdf", "downloads/example.com/pdf/files/board/2026.pdf"},
		{"https://blog.example.com/a/b/c/report.PDF", "pdf", "downloads/example.com/pdf/a/b/c/report.PDF"},
		{"https://example.com/top.docx", "docx", "downloads/example.com/docx/top.docx"},
		{"https://example.com/dir/../x/y.zip", "zip", "downloads/example.com/zip/x/y.zip"},
		{"https://example.com/path%20with%20space/my%20file.pdf", "pdf", "downloads/example.com/pdf/path with space/my file.pdf"},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		got := c.destPath(u, tc.ext)
		// Compare with OS-agnostic separators.
		if strings.ReplaceAll(got, "\\", "/") != tc.want {
			t.Errorf("destPath(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestUrlExt(t *testing.T) {
	cases := map[string]string{
		"https://x.com/a.PDF":        "pdf",
		"https://x.com/a/b/file.zip": "zip",
		"https://x.com/noext":        "",
		"https://x.com/a.tar.gz":     "gz",
		"https://x.com/dir/":         "",
	}
	for raw, want := range cases {
		u, _ := url.Parse(raw)
		if got := urlExt(u); got != want {
			t.Errorf("urlExt(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestExtractLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/dir/page.html")
	doc := `<html><body>
		<a href="report.pdf">rel</a>
		<a href="/abs/file.docx">abs</a>
		<a href="https://other.com/x.pdf">external</a>
		<a href="mailto:a@b.com">mail</a>
		<a href="#frag">frag</a>
		<img src="/img/logo.png">
	</body></html>`
	got := extractLinks(strings.NewReader(doc), base)
	want := map[string]bool{
		"https://example.com/dir/report.pdf": true,
		"https://example.com/abs/file.docx":  true,
		"https://other.com/x.pdf":            true,
		"https://example.com/dir/page.html":  true, // #frag resolves to the page itself
		"https://example.com/img/logo.png":   true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d links %v, want %d", len(got), urlList(got), len(want))
	}
	for _, u := range got {
		if !want[u.String()] {
			t.Errorf("unexpected link %q", u)
		}
	}
}

func urlList(us []*url.URL) []string {
	out := make([]string, len(us))
	for i, u := range us {
		out[i] = u.String()
	}
	return out
}
