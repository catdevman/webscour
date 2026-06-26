package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/temoto/robotstxt"
)

// robotsManager fetches and caches robots.txt rules per host and enforces a
// per-host crawl delay. Hosts are loaded lazily and exactly once; concurrent
// callers for the same host block until the rules are ready.
type robotsManager struct {
	ua     string
	client *http.Client

	mu    sync.Mutex
	hosts map[string]*hostRules
}

// hostRules holds the parsed robots group for one host plus a serializing
// limiter that spaces out requests according to the host's Crawl-delay.
type hostRules struct {
	ready   chan struct{}    // closed once group/limiter are populated
	group   *robotstxt.Group // nil means "allow everything"
	limiter *hostLimiter
	// rules mirrors the Allow/Disallow directives of the agent group actually
	// selected for this host. The robotstxt library does not expose the rule
	// that matched a path, so we keep our own copy solely to explain disallow
	// decisions in log messages. It is populated alongside group in load().
	rules []explRule
}

func newRobotsManager(ua string, client *http.Client) *robotsManager {
	return &robotsManager{ua: ua, client: client, hosts: map[string]*hostRules{}}
}

// rulesFor returns the (lazily loaded) rules for the host of u.
func (rm *robotsManager) rulesFor(u *url.URL) *hostRules {
	key := u.Scheme + "://" + u.Host

	rm.mu.Lock()
	hr := rm.hosts[key]
	if hr == nil {
		hr = &hostRules{ready: make(chan struct{}), limiter: &hostLimiter{}}
		rm.hosts[key] = hr
		go rm.load(u, hr)
	}
	rm.mu.Unlock()

	<-hr.ready
	return hr
}

// allowed reports whether the crawler may fetch u under robots.txt rules.
func (rm *robotsManager) allowed(u *url.URL) bool {
	hr := rm.rulesFor(u)
	if hr.group == nil {
		return true
	}
	return hr.group.Test(u.EscapedPath())
}

// explainDisallow returns a human-readable description of the robots.txt rule
// that blocks u, e.g. `Disallow: /private/`. It returns "" if no Disallow rule
// is found to match (the decision then came from something other than an
// explicit rule, such as a 5xx robots.txt response).
func (rm *robotsManager) explainDisallow(u *url.URL) string {
	hr := rm.rulesFor(u)
	r := findExplRule(hr.rules, u.EscapedPath())
	if r == nil || r.allow {
		return ""
	}
	return r.describe()
}

// wait blocks until the per-host crawl delay (if any) has elapsed since the
// previous request to that host.
func (rm *robotsManager) wait(u *url.URL) {
	rm.rulesFor(u).limiter.wait()
}

// load fetches and parses robots.txt for the host. A missing file (or any 4xx)
// is treated as "allow all"; transport failures are also treated permissively
// so a single flaky robots.txt does not abort the whole crawl.
func (rm *robotsManager) load(u *url.URL, hr *hostRules) {
	defer close(hr.ready)

	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	req, err := http.NewRequest(http.MethodGet, robotsURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", rm.ua)

	resp, err := rm.client.Do(req)
	if err != nil {
		log.Printf("robots: %s: %v (assuming allow-all)", robotsURL, err)
		return
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Printf("robots: %s: %v (assuming allow-all)", robotsURL, err)
		return
	}

	data, err := robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		log.Printf("robots: %s: parse error: %v (assuming allow-all)", robotsURL, err)
		return
	}

	group := data.FindGroup(rm.ua)
	hr.group = group
	if group != nil && group.CrawlDelay > 0 {
		hr.limiter.delay = group.CrawlDelay
		log.Printf("robots: %s crawl-delay %s", u.Host, group.CrawlDelay)
	}

	// Re-derive the directives of the selected agent group so we can later name
	// the specific rule responsible for any disallow (the library hides this).
	hr.rules = agentRules(body, rm.ua)
}

// explRule is one parsed Allow/Disallow directive, kept solely to explain in log
// messages which robots.txt rule blocked a URL. It mirrors the matching
// semantics of the robotstxt library (whose own rules are unexported): plain
// rules match by path prefix; rules containing * or $ match via a compiled
// pattern; the longest match wins.
type explRule struct {
	allow   bool
	raw     string         // directive value exactly as written, for display
	norm    string         // normalized prefix (empty when pattern != nil)
	pattern *regexp.Regexp // non-nil for wildcard (* / $) rules
	weight  int            // match length used for longest-match precedence
}

// describe renders the rule the way it appears in robots.txt, e.g.
// "Disallow: /private/" or "Disallow: /*.pdf$ (pattern)".
func (r *explRule) describe() string {
	kind := "Disallow"
	if r.allow {
		kind = "Allow"
	}
	if r.pattern != nil {
		return fmt.Sprintf("%s: %s (pattern)", kind, r.raw)
	}
	return fmt.Sprintf("%s: %s", kind, r.raw)
}

// agentRules parses robots.txt and returns the Allow/Disallow rules of the group
// selected for ua, applying the same group-selection rule as the library: the
// longest user-agent token that is a prefix of ua wins, with "*" as the weakest
// fallback.
func agentRules(body []byte, ua string) []explRule {
	groups := map[string][]explRule{}
	var current []string // agent tokens of the group being parsed
	sawMember := false    // a rule appeared since the last user-agent line

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, val, ok := splitDirective(line)
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "user-agent", "useragent":
			// Consecutive user-agent lines share a group until a member line
			// (allow/disallow/crawl-delay) ends it.
			if sawMember {
				current = nil
				sawMember = false
			}
			current = append(current, strings.ToLower(strings.TrimSpace(val)))
		case "allow", "disallow":
			sawMember = true
			if r, ok := makeExplRule(val, strings.EqualFold(key, "allow")); ok {
				for _, a := range current {
					groups[a] = append(groups[a], r)
				}
			}
		case "crawl-delay", "crawldelay":
			sawMember = true
		}
	}

	// Select the applicable group exactly as robotstxt.FindGroup does.
	agent := strings.ToLower(ua)
	var ret []explRule
	best := 0
	if g, ok := groups["*"]; ok {
		ret, best = g, 1
	}
	for a, g := range groups {
		if a != "*" && a != "" && strings.HasPrefix(agent, a) && len(a) > best {
			ret, best = g, len(a)
		}
	}
	return ret
}

// splitDirective splits a robots.txt line into key and value on the first colon
// (falling back to the first run of whitespace). It returns ok=false for blank
// or value-less lines.
func splitDirective(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	if k, v, found := strings.Cut(line, ":"); found {
		return strings.TrimSpace(k), strings.TrimSpace(v), true
	}
	if i := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' }); i >= 0 {
		return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
	}
	return "", "", false
}

// makeExplRule normalizes a directive value the same way the robotstxt parser
// does and compiles wildcard patterns. An empty value yields ok=false (an empty
// Disallow/Allow is ignored per the spec).
func makeExplRule(val string, allow bool) (explRule, bool) {
	raw := strings.TrimSpace(val)
	if raw == "" {
		return explRule{}, false
	}
	p := raw
	if !strings.HasPrefix(p, "*") && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.TrimRight(p, "*")
	if strings.ContainsAny(p, "*$") {
		q := regexp.QuoteMeta(p)
		q = strings.ReplaceAll(q, `\*`, `.*`)
		q = strings.ReplaceAll(q, `\$`, `$`)
		re, err := regexp.Compile(q)
		if err != nil {
			return explRule{}, false
		}
		return explRule{allow: allow, raw: raw, pattern: re, weight: len(q)}, true
	}
	return explRule{allow: allow, raw: raw, norm: p, weight: len(p)}, true
}

// findExplRule returns the rule that decides path under longest-match precedence,
// mirroring robotstxt.Group.findRule. It returns nil if no rule applies.
func findExplRule(rules []explRule, path string) *explRule {
	var ret *explRule
	best := 0
	for i := range rules {
		r := &rules[i]
		switch {
		case r.pattern != nil:
			if r.pattern.MatchString(path) && r.weight > best {
				ret, best = r, r.weight
			}
		case r.norm == "/" && best == 0:
			ret, best = r, 1
		case strings.HasPrefix(path, r.norm):
			if r.weight > best {
				ret, best = r, r.weight
			}
		}
	}
	return ret
}

// hostLimiter serializes requests to a single host and enforces a minimum
// spacing (delay) between them.
type hostLimiter struct {
	mu    sync.Mutex
	delay time.Duration
	last  time.Time
}

func (h *hostLimiter) wait() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.delay <= 0 {
		return
	}
	if !h.last.IsZero() {
		if d := h.delay - time.Since(h.last); d > 0 {
			time.Sleep(d)
		}
	}
	h.last = time.Now()
}
