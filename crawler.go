package main

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/net/html"
)

// crawler coordinates the concurrent crawl. Each discovered URL is processed in
// its own goroutine; the sem channel caps the number of simultaneous in-flight
// HTTP requests, while per-host crawl delays are enforced by the robotsManager.
type crawler struct {
	cfg    config
	root   string // registrable domain (eTLD+1) that bounds the crawl
	client *http.Client
	robots *robotsManager

	sem  chan struct{}  // bounded concurrency for actual HTTP fetches
	wg   sync.WaitGroup // tracks all outstanding URL tasks
	seen sync.Map       // url string -> struct{}; ensures each URL handled once

	pagesScanned    atomic.Int64
	filesDownloaded atomic.Int64
	filesSkipped    atomic.Int64
	errors          atomic.Int64
}

func newCrawler(cfg config, root string) *crawler {
	client := newHTTPClient(cfg)
	return &crawler{
		cfg:    cfg,
		root:   root,
		client: client,
		robots: newRobotsManager(cfg.userAgent, client),
		sem:    make(chan struct{}, cfg.workers),
	}
}

// submit schedules u for processing unless it has already been seen. The URL is
// normalized before deduplication (fragment stripped, host lower-cased, empty
// path treated as "/") so that #anchors, host-case variants, and the
// "http://host" vs "http://host/" forms all collapse to a single visit.
func (c *crawler) submit(u *url.URL) {
	normalizeURL(u)
	key := u.String()
	if _, dup := c.seen.LoadOrStore(key, struct{}{}); dup {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.handle(u)
	}()
}

// normalizeURL canonicalizes a URL in place for deduplication so that
// equivalent forms of the same resource map to one key: the fragment is
// dropped, the host is lower-cased, and an empty path becomes "/".
func normalizeURL(u *url.URL) {
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
}

// handle dispatches a single URL to either the downloader or the page scanner,
// after applying scope and robots.txt checks.
func (c *crawler) handle(u *url.URL) {
	if !c.inScope(u) {
		return
	}
	if !c.robots.allowed(u) {
		if rule := c.robots.explainDisallow(u); rule != "" {
			log.Printf("robots: disallowed %s (matched %s)", u, rule)
		} else {
			log.Printf("robots: disallowed %s (no matching Allow/Disallow rule; e.g. robots.txt 5xx or whole-site block)", u)
		}
		return
	}

	if ext, ok := c.downloadExt(u); ok {
		c.download(u, ext)
		return
	}
	c.scanPage(u)
}

// scanPage fetches an HTML page and submits every in-scope link it contains.
func (c *crawler) scanPage(u *url.URL) {
	c.robots.wait(u)

	c.sem <- struct{}{}
	resp, err := c.fetch(u)
	if err != nil {
		<-c.sem
		log.Printf("fetch: %s: %v", u, err)
		c.errors.Add(1)
		return
	}

	// Only parse HTML responses; skip binaries we did not classify as files.
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK || !strings.Contains(ct, "text/html") {
		resp.Body.Close()
		<-c.sem
		return
	}

	links := extractLinks(resp.Body, resp.Request.URL)
	resp.Body.Close()
	<-c.sem

	c.pagesScanned.Add(1)
	for _, link := range links {
		c.submit(link)
	}
}

// download streams a matching file to disk, skipping it if it already exists.
func (c *crawler) download(u *url.URL, ext string) {
	dest := c.destPath(u, ext)
	if exists(dest) {
		c.filesSkipped.Add(1)
		return
	}

	c.robots.wait(u)

	c.sem <- struct{}{}
	defer func() { <-c.sem }()

	resp, err := c.fetch(u)
	if err != nil {
		log.Printf("download: %s: %v", u, err)
		c.errors.Add(1)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("download: %s: status %d", u, resp.StatusCode)
		c.errors.Add(1)
		return
	}

	n, err := saveToFile(dest, resp.Body)
	if err != nil {
		log.Printf("download: %s: %v", u, err)
		c.errors.Add(1)
		return
	}
	c.filesDownloaded.Add(1)
	log.Printf("downloaded %s (%d bytes) -> %s", u, n, dest)
}

// fetch issues a GET request with the configured User-Agent.
func (c *crawler) fetch(u *url.URL) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.cfg.userAgent)
	return c.client.Do(req)
}

// inScope reports whether u is an http(s) URL whose host shares the crawl's
// registrable domain (so subdomains are included, external sites excluded).
func (c *crawler) inScope(u *url.URL) bool {
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return registrableDomain(u.Host) == c.root
}

// downloadExt returns the matching download extension for u, if any.
func (c *crawler) downloadExt(u *url.URL) (string, bool) {
	ext := urlExt(u)
	if ext != "" && c.cfg.extSet[ext] {
		return ext, true
	}
	return "", false
}

// extractLinks tokenizes an HTML document and returns absolute URLs found in
// href and src attributes, resolved against base.
func extractLinks(r io.Reader, base *url.URL) []*url.URL {
	var out []*url.URL
	seen := map[string]bool{}
	z := html.NewTokenizer(r)
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			for _, a := range tok.Attr {
				if a.Key != "href" && a.Key != "src" {
					continue
				}
				ref, err := url.Parse(strings.TrimSpace(a.Val))
				if err != nil {
					continue
				}
				abs := base.ResolveReference(ref)
				abs.Fragment = ""
				if abs.Scheme != "http" && abs.Scheme != "https" {
					continue
				}
				if s := abs.String(); !seen[s] {
					seen[s] = true
					out = append(out, abs)
				}
			}
		}
	}
}
