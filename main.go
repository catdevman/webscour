// Command webscour crawls a website starting from a single URL, follows links
// within the same registrable domain (including subdomains), and downloads any
// files whose extension matches a configured set. Downloaded files are written
// to a directory tree that mirrors their location on the site:
//
//	<out>/<root-domain>/<ext>/<file url path dirs>/<filename>
//
// e.g. https://www.example.com/files/board/2026.pdf becomes
//
//	./downloads/example.com/pdf/files/board/2026.pdf
//
// The crawler respects robots.txt (Allow/Disallow and per-host Crawl-delay),
// scans every page at most once, and runs as concurrently as the worker pool
// and per-host crawl delays allow.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Build metadata, injected at release time via -ldflags by GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "webscour:", err)
		flag.Usage()
		os.Exit(2)
	}

	start, err := url.Parse(cfg.startURL)
	if err != nil || (start.Scheme != "http" && start.Scheme != "https") {
		log.Fatalf("invalid -url %q: must be an absolute http(s) URL", cfg.startURL)
	}

	root := registrableDomain(start.Host)

	c := newCrawler(cfg, root)
	log.Printf("webscour %s starting crawl of %s (domain scope: %s, extensions: %s)",
		version, start, root, strings.Join(cfg.exts, ", "))

	startTime := time.Now()
	c.submit(start)
	c.wg.Wait()

	log.Printf("done in %s — pages scanned: %d, files downloaded: %d, skipped (exists): %d, errors: %d",
		time.Since(startTime).Round(time.Millisecond),
		c.pagesScanned.Load(), c.filesDownloaded.Load(),
		c.filesSkipped.Load(), c.errors.Load())
}

// config holds the runtime configuration assembled from CLI flags.
type config struct {
	startURL  string
	exts      []string        // normalized, lower-case, no leading dot
	extSet    map[string]bool // membership test for exts
	workers   int
	outDir    string
	userAgent string
	timeout   time.Duration
}

func parseFlags() (config, error) {
	var (
		startURL    = flag.String("url", "", "starting URL to crawl (required, absolute http/https)")
		exts        = flag.String("ext", "pdf", "comma-separated file extensions to download, e.g. pdf,docx,zip")
		workers     = flag.Int("workers", 16, "maximum number of concurrent in-flight HTTP requests")
		outDir      = flag.String("out", "downloads", "output directory for downloaded files")
		ua          = flag.String("ua", "webscour/1.0 (+https://github.com/catdevman/webscour)", "User-Agent header / robots.txt agent token")
		timeout     = flag.Duration("timeout", 30*time.Second, "per-request HTTP timeout")
		showVersion = flag.Bool("version", false, "print version information and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("webscour %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	if strings.TrimSpace(*startURL) == "" {
		return config{}, fmt.Errorf("-url is required")
	}

	cfg := config{
		startURL:  *startURL,
		workers:   *workers,
		outDir:    *outDir,
		userAgent: *ua,
		timeout:   *timeout,
		extSet:    map[string]bool{},
	}
	if cfg.workers < 1 {
		cfg.workers = 1
	}
	for _, e := range strings.Split(*exts, ",") {
		e = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(e, ".")))
		if e == "" || cfg.extSet[e] {
			continue
		}
		cfg.extSet[e] = true
		cfg.exts = append(cfg.exts, e)
	}
	if len(cfg.exts) == 0 {
		return config{}, fmt.Errorf("-ext must list at least one extension")
	}
	return cfg, nil
}

// newHTTPClient builds the shared client. Redirects are followed automatically;
// each request carries the configured timeout.
func newHTTPClient(cfg config) *http.Client {
	return &http.Client{Timeout: cfg.timeout}
}

// registrableDomain returns the eTLD+1 ("example.com") for a host. For hosts
// without a registrable domain — IP literals, localhost, single-label internal
// names — it falls back to the bare hostname so the crawl scope is still
// well-defined.
func registrableDomain(host string) string {
	h := hostname(host)
	// IP literals (and localhost) have no registrable domain; publicsuffix
	// would return a nonsense value for IPs, so handle them as the literal host.
	if h == "localhost" || net.ParseIP(strings.Trim(h, "[]")) != nil {
		return h
	}
	if e, err := publicsuffix.EffectiveTLDPlusOne(h); err == nil {
		return e
	}
	return h
}

// hostname strips an optional :port from a host[:port] string.
func hostname(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		// Guard against IPv6 literals like [::1]:80 — only strip if it looks
		// like a real port suffix.
		if !strings.Contains(host[i:], "]") {
			return strings.ToLower(host[:i])
		}
	}
	return strings.ToLower(host)
}
