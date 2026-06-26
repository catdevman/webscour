package main

import (
	"testing"

	"github.com/temoto/robotstxt"
)

// TestExplainAgreesWithLibrary guards against the re-derived rule matcher
// drifting from the robotstxt library's authoritative allow/deny decision: for
// every path, a Disallow explanation must coincide with the library blocking it.
func TestExplainAgreesWithLibrary(t *testing.T) {
	robots := `
User-agent: *
Disallow: /

User-agent: webscour
Disallow: /private/
Allow: /private/public/
Disallow: /*.zip$
`
	const ua = "webscour/1.0"
	data, err := robotstxt.FromString(robots)
	if err != nil {
		t.Fatal(err)
	}
	group := data.FindGroup(ua)
	rules := agentRules([]byte(robots), ua)

	for _, p := range []string{
		"/private/secret.pdf", "/private/public/ok.pdf", "/a/b.zip",
		"/index.html", "/", "/private/", "/x.zipper",
	} {
		libAllowed := group.Test(p)
		r := findExplRule(rules, p)
		explBlocked := r != nil && !r.allow
		if libAllowed == explBlocked {
			t.Errorf("path %q: library allowed=%v but explainer blocked=%v (rule %+v)", p, libAllowed, explBlocked, r)
		}
	}
}

// TestExplainDisallow verifies that the rule re-derivation agrees with the
// robotstxt library's decision and names the specific offending directive,
// including agent-group selection, longest-match precedence, and wildcards.
func TestExplainDisallow(t *testing.T) {
	robots := `
User-agent: *
Disallow: /

User-agent: webscour
Disallow: /private/
Allow: /private/public/
Disallow: /*.zip$
`
	rules := agentRules([]byte(robots), "webscour/1.0 (+https://example.com)")

	cases := []struct {
		path string
		want string // "" means not blocked by a Disallow
	}{
		{"/private/secret.pdf", "Disallow: /private/"},
		{"/private/public/ok.pdf", ""},                  // Allow overrides (longer match)
		{"/downloads/archive.zip", "Disallow: /*.zip$ (pattern)"},
		{"/index.html", ""},                             // webscour group has no blanket Disallow: /
	}
	for _, tc := range cases {
		r := findExplRule(rules, tc.path)
		got := ""
		if r != nil && !r.allow {
			got = r.describe()
		}
		if got != tc.want {
			t.Errorf("path %q: got %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestExplainDisallowWildcardAgentFallback confirms the "*" group is used when
// no specific agent group matches.
func TestExplainDisallowWildcardAgentFallback(t *testing.T) {
	robots := "User-agent: *\nDisallow: /no-bots/\n"
	rules := agentRules([]byte(robots), "some-other-bot/2.0")
	if r := findExplRule(rules, "/no-bots/page"); r == nil || r.allow || r.describe() != "Disallow: /no-bots/" {
		t.Fatalf("expected Disallow: /no-bots/, got %+v", r)
	}
	if r := findExplRule(rules, "/allowed/page"); r != nil {
		t.Errorf("expected no rule for /allowed/page, got %+v", r)
	}
}
