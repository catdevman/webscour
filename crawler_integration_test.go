package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestRecursiveCrawl spins up a small multi-page site and verifies the crawler
// walks links transitively (page1 -> page2 -> page3), scans each page exactly
// once even when multiple pages link to it, stays on-domain, and downloads
// matching files.
func TestRecursiveCrawl(t *testing.T) {
	var hits int64
	mux := http.NewServeMux()

	page := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&hits, 1)
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(body))
		}
	}

	mux.HandleFunc("/", page(`<a href="/page2">2</a> <a href="/a.pdf">pdf</a>`))
	mux.HandleFunc("/page2", page(`<a href="/page3">3</a> <a href="/">home</a>`))
	// page3 links back to page2 and to an off-domain site.
	mux.HandleFunc("/page3", page(`<a href="/page2">2</a> <a href="https://example.com/x">ext</a> <a href="/b.pdf">pdf</a>`))
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/a.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("%PDF-a"))
	})
	mux.HandleFunc("/b.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("%PDF-b"))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := t.TempDir()
	u, _ := url.Parse(srv.URL)
	cfg := config{
		startURL:  srv.URL,
		exts:      []string{"pdf"},
		extSet:    map[string]bool{"pdf": true},
		workers:   8,
		outDir:    out,
		userAgent: "webscour-test",
		timeout:   10 * time.Second,
	}

	c := newCrawler(cfg, registrableDomain(u.Host))
	start, _ := url.Parse(srv.URL)
	c.submit(start)

	done := make(chan struct{})
	go func() { c.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("crawl did not finish (possible deadlock / non-termination)")
	}

	if got := c.pagesScanned.Load(); got != 3 {
		t.Errorf("pagesScanned = %d, want 3 (/, /page2, /page3)", got)
	}
	if got := c.filesDownloaded.Load(); got != 2 {
		t.Errorf("filesDownloaded = %d, want 2 (a.pdf, b.pdf)", got)
	}

	root := registrableDomain(u.Host)
	for _, name := range []string{"a.pdf", "b.pdf"} {
		p := filepath.Join(out, root, "pdf", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected downloaded file %s: %v", p, err)
		}
	}
}
