package fuzzy

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestOptimalNonGreedyAlignment(t *testing.T) {
	text := "~/personal/code/igwtcode/workstation"
	res := Match(text, "wstat", SchemePath, true)
	if !res.Matched {
		t.Fatalf("expected wstat to match %s", text)
	}
	baseIdx := len([]rune("~/personal/code/igwtcode/"))
	if res.Start != baseIdx {
		t.Fatalf("expected alignment to start at workstation (%d), got start=%d positions=%v", baseIdx, res.Start, res.Positions)
	}

	direct := Match("acme-prod-admin", "proda", SchemeDefault, true)
	prefixed := Match("acme-preprod-admin", "proda", SchemeDefault, true)
	if !direct.Matched || !prefixed.Matched {
		t.Fatalf("expected both prod candidates to match")
	}
	if direct.Score <= prefixed.Score {
		t.Fatalf("expected acme-prod-admin (%d) > acme-preprod-admin (%d)", direct.Score, prefixed.Score)
	}
}

func TestSchemePathAndConsecutiveBoundaryBonuses(t *testing.T) {
	res := Match("~/work/code/work", "work", SchemePath, true)
	if !res.Matched {
		t.Fatalf("expected match")
	}
	wantPos := []int{12, 13, 14, 15}
	if !reflect.DeepEqual(res.Positions, wantPos) {
		t.Fatalf("expected SchemePath tie to pick basename segment %v, got %v", wantPos, res.Positions)
	}

	itf := Match("infra-terraform", "itf", SchemeDefault, true)
	mid := Match("gitfetch", "itf", SchemeDefault, true)
	if !itf.Matched || !mid.Matched {
		t.Fatalf("expected both to match")
	}
	if itf.Score <= mid.Score {
		t.Fatalf("expected boundary match infra-terraform (%d) > mid-word gitfetch (%d)", itf.Score, mid.Score)
	}

	camel := Match("myAppService", "mA", SchemeDefault, true)
	if !camel.Matched {
		t.Fatalf("expected CamelCase smart-case match")
	}
	if miss := Match("myappservice", "mA", SchemeDefault, false); miss.Matched {
		t.Fatalf("expected smart-case uppercase query to be case-sensitive")
	}
}

func TestParseQueryAndExtendedModifiers(t *testing.T) {
	tokens := ParseQuery("  work !docs 'prod ^api cli$ ^exact$  ")
	if len(tokens) != 6 {
		t.Fatalf("expected 6 tokens, got %d: %+v", len(tokens), tokens)
	}
	if tokens[0].Kind != TokenFuzzy || tokens[0].Negate || tokens[0].CaseSensitive {
		t.Fatalf("unexpected token[0]: %+v", tokens[0])
	}
	if tokens[1].Kind != TokenFuzzy || !tokens[1].Negate || tokens[1].Pattern != "docs" {
		t.Fatalf("unexpected token[1]: %+v", tokens[1])
	}
	if tokens[2].Kind != TokenExact || tokens[2].Pattern != "prod" {
		t.Fatalf("unexpected token[2]: %+v", tokens[2])
	}
	if tokens[3].Kind != TokenPrefix || tokens[3].Pattern != "api" {
		t.Fatalf("unexpected token[3]: %+v", tokens[3])
	}
	if tokens[4].Kind != TokenSuffix || tokens[4].Pattern != "cli" {
		t.Fatalf("unexpected token[4]: %+v", tokens[4])
	}
	if tokens[5].Kind != TokenEqual || tokens[5].Pattern != "exact" {
		t.Fatalf("unexpected token[5]: %+v", tokens[5])
	}

	if res := MatchToken("notes-cli", tokens[4], SchemeDefault, true); !res.Matched || !reflect.DeepEqual(res.Positions, []int{6, 7, 8}) {
		t.Fatalf("unexpected suffix match: %+v", res)
	}
	if res := MatchToken("notes-cli-v2", tokens[4], SchemeDefault, false); res.Matched {
		t.Fatalf("expected suffix cli$ not to match notes-cli-v2")
	}
	if res := MatchToken("ws-prod-ro", tokens[2], SchemeDefault, false); !res.Matched {
		t.Fatalf("expected exact 'prod to match ws-prod-ro")
	}
	if res := MatchToken("ws-pr-od", tokens[2], SchemeDefault, false); res.Matched {
		t.Fatalf("expected exact 'prod not to fuzzy-match ws-pr-od")
	}
	if res := MatchToken("exact", tokens[5], SchemeDefault, false); !res.Matched {
		t.Fatalf("expected ^exact$ to match exact")
	}
	if res := MatchToken("exact-extra", tokens[5], SchemeDefault, false); res.Matched {
		t.Fatalf("expected ^exact$ not to match exact-extra")
	}
}

func TestPositionsMultiByteUTF8AndAggregation(t *testing.T) {
	text := "ümlaut-workstation"
	tokens := ParseQuery("wstat 'mlaut")
	pos := MatchPositions(text, tokens, SchemeDefault)

	want := []int{1, 2, 3, 4, 5, 7, 11, 12, 13, 14}
	if !reflect.DeepEqual(pos, want) {
		t.Fatalf("expected rune positions %v, got %v", want, pos)
	}
}

func TestParseFilterQuery(t *testing.T) {
	q := ParseFilterQuery(`state:open,draft repo:example/demo-web "health check"`)
	if !slices.Equal(q.Tokens["state"], []string{"open", "draft"}) {
		t.Fatalf("unexpected state tokens: %+v", q.Tokens["state"])
	}
	if !slices.Equal(q.Tokens["repo"], []string{"example/demo-web"}) {
		t.Fatalf("unexpected repo tokens: %+v", q.Tokens["repo"])
	}
	if !slices.Equal(q.Terms, []string{"health check"}) {
		t.Fatalf("unexpected free terms: %+v", q.Terms)
	}
}

func TestScoreRankingAndBoundaries(t *testing.T) {
	exact, okExact := Score("api-service", "api-service")
	prefix, okPref := Score("api-service-worker", "api-service")
	wordBound, okWB := Score("my-api-service", "api")
	subseq, okSub := Score("alpha-beta-iota", "abi")
	_, okMiss := Score("demo-web", "xyz")

	if !okExact || !okPref || !okWB || !okSub || okMiss {
		t.Fatalf("unexpected match booleans: exact=%v pref=%v wb=%v sub=%v miss=%v",
			okExact, okPref, okWB, okSub, okMiss)
	}
	if !(exact > prefix && prefix > wordBound && wordBound > subseq) {
		t.Fatalf("expected ranking exact(%d) > prefix(%d) > wordBound(%d) > subseq(%d)",
			exact, prefix, wordBound, subseq)
	}

	boundaryPairs := [][3]string{
		{"my-apple-api", "workstation-application-list", "api"},
		{"workstation", "my-awesome-site", "ws"},
		{"test-suite", "https://example.invalid/org/awesome-site", "test"},
	}
	for _, tc := range boundaryPairs {
		sGood, okGood := Score(tc[0], tc[2])
		sNoise, okNoise := Score(tc[1], tc[2])
		if !okGood {
			t.Fatalf("expected Score(%q, %q) to match", tc[0], tc[2])
		}
		if okNoise && sGood <= sNoise {
			t.Fatalf("expected %q (%d) to outrank scattered %q (%d) for %q", tc[0], sGood, tc[1], sNoise, tc[2])
		}
	}

	if _, ok := Score("/srv/personal/code/other-repo", "ws"); ok {
		t.Fatalf("expected no match when pattern characters are absent")
	}

	shouldMatch := [][2]string{
		{"demo-web", "dw"},
		{"my-apple-api", "mapi"},
		{"font-caskaydia-cove-nerd-font", "cask"},
		{"FilterGroupState", "fgs"},
	}
	for _, tc := range shouldMatch {
		if score, ok := Score(tc[0], tc[1]); !ok || score <= 0 {
			t.Fatalf("expected Score(%q, %q) to match, got score=%d ok=%v", tc[0], tc[1], score, ok)
		}
	}
}

func TestFilterWithTagsAndRanking(t *testing.T) {
	type sampleRow struct {
		name   string
		kind   string
		exit   string
		secret string
		status string
	}

	rows := []sampleRow{
		{name: "cargo-audit-helper", kind: "brew", exit: "0", secret: "false", status: "installed"},
		{name: "api-service-token", kind: "dotenv", exit: "2", secret: "true", status: "missing"},
		{name: "api-service", kind: "repo", exit: "0", secret: "false", status: "ok"},
		{name: "my-api-gateway", kind: "repo", exit: "1", secret: "true", status: "stale"},
	}

	extract := func(r sampleRow) ([]string, map[string]string) {
		return []string{r.name, r.kind, r.status}, map[string]string{
			"kind":   r.kind,
			"exit":   r.exit,
			"secret": r.secret,
			"status": r.status,
		}
	}

	ranked := Filter(rows, "api-service", extract)
	if len(ranked) != 2 || ranked[0].name != "api-service" || ranked[1].name != "api-service-token" {
		t.Fatalf("unexpected ranked results: %+v", ranked)
	}

	errs := Filter(rows, "exit:err", extract)
	if len(errs) != 2 || errs[0].name != "api-service-token" || errs[1].name != "my-api-gateway" {
		t.Fatalf("expected exit:err to match non-zero rows, got %+v", errs)
	}

	oks := Filter(rows, "exit:ok kind:repo", extract)
	if len(oks) != 1 || oks[0].name != "api-service" {
		t.Fatalf("expected exit:ok kind:repo to match api-service, got %+v", oks)
	}

	sec := Filter(rows, "secret:yes status:missing,installed", extract)
	if len(sec) != 1 || sec[0].name != "api-service-token" {
		t.Fatalf("expected secret:yes status:missing,installed to match api-service-token, got %+v", sec)
	}
}

func TestLongInputsOver256RunesMatch(t *testing.T) {
	prefix := strings.Repeat("segment-padding-", 24)
	longText := prefix + "target-needle-endpoint"
	if len([]rune(longText)) <= 256 {
		t.Fatalf("test setup expected >256 runes, got %d", len([]rune(longText)))
	}

	if score, ok := Score(longText, "tne"); !ok || score <= 0 {
		t.Fatalf("expected Score to match >256 rune input, got score=%d ok=%v", score, ok)
	}
	res := Match(longText, "target-needle", SchemePath, true)
	if !res.Matched || res.Start != len([]rune(prefix)) || len(res.Positions) != len("target-needle") {
		t.Fatalf("expected Match to find target-needle past 256 runes, got %+v", res)
	}
	items := []string{longText, "unrelated-short"}
	filtered := Filter(items, "tne", func(s string) ([]string, map[string]string) {
		return []string{s}, nil
	})
	if len(filtered) != 1 || filtered[0] != longText {
		t.Fatalf("expected Filter to match >256 rune input, got %+v", filtered)
	}
}

func TestConcurrentSchemesRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	schemes := []Scheme{SchemeDefault, SchemePath}
	inputs := []string{
		"~/personal/code/igwtcode/workstation",
		"acme-prod-admin",
		"ümlaut-workstation",
		strings.Repeat("long-prefix/", 30) + "service-alpha",
	}
	queries := []string{"wstat", "proda", "'mlaut", "^acme", "alpha$", "sa"}

	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				sc := schemes[(id+i)%len(schemes)]
				txt := inputs[(id+i)%len(inputs)]
				q := queries[(id+i)%len(queries)]
				toks := ParseQuery(q)
				_ = Match(txt, q, sc, i%2 == 0)
				_ = MatchPositions(txt, toks, sc)
				_, _ = Score(txt, q)
			}
		}(g)
	}
	wg.Wait()
}
