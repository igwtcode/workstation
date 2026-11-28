package fuzzy

import (
	"sort"
	"strings"
	"unicode"
)

type FilterQuery struct {
	Raw    string
	Terms  []string
	Tokens map[string][]string
}

func ParseFilterQuery(raw string) FilterQuery {
	fq := FilterQuery{
		Raw:    strings.TrimSpace(raw),
		Tokens: map[string][]string{},
	}
	if fq.Raw == "" {
		return fq
	}

	fields := SplitFields(fq.Raw)
	for _, f := range fields {
		if k, v, ok := strings.Cut(f, ":"); ok && k != "" && v != "" && !strings.ContainsAny(k, " /\\") {
			key := strings.ToLower(strings.TrimSpace(k))
			for _, part := range strings.Split(v, ",") {
				part = strings.ToLower(strings.TrimSpace(part))
				if part != "" {
					fq.Tokens[key] = append(fq.Tokens[key], part)
				}
			}
			continue
		}
		fq.Terms = append(fq.Terms, strings.ToLower(f))
	}
	return fq
}

func SplitFields(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}

	for _, r := range s {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else if quote == '"' && r == '\\' {
				escaped = true
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '"' || r == '\'':
			quote = r
		case r == '\\':
			escaped = true
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func Score(haystack, needle string) (int, bool) {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return 0, true
	}
	if haystack == "" {
		return 0, false
	}
	res := MatchToken(haystack, Token{
		Raw:           needle,
		Pattern:       needle,
		Kind:          TokenFuzzy,
		CaseSensitive: false,
	}, SchemeDefault, false)
	if !res.Matched {
		return 0, false
	}
	return res.Score, true
}

func Filter[T any](items []T, raw string, extract func(T) (fields []string, tags map[string]string)) []T {
	fq := ParseFilterQuery(raw)
	if len(fq.Terms) == 0 && len(fq.Tokens) == 0 {
		out := make([]T, len(items))
		copy(out, items)
		return out
	}

	type scored struct {
		item  T
		score int
	}
	matches := make([]scored, 0, len(items))

	for _, it := range items {
		fields, tags := extract(it)
		if !matchFilterTokens(fq.Tokens, tags) {
			continue
		}
		totalScore := 0
		ok := true
		for _, term := range fq.Terms {
			best := -1
			matched := false
			for fi, fld := range fields {
				if fld == "" {
					continue
				}
				if s, m := Score(fld, term); m {
					cand := s + max(0, (4-fi)*8)
					if !matched || cand > best {
						best = cand
						matched = true
					}
				}
			}
			if !matched {
				ok = false
				break
			}
			totalScore += best
		}
		if ok {
			matches = append(matches, scored{item: it, score: totalScore})
		}
	}

	if len(fq.Terms) > 0 {
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].score > matches[j].score
		})
	}

	out := make([]T, len(matches))
	for i, m := range matches {
		out[i] = m.item
	}
	return out
}

func matchFilterTokens(tokens map[string][]string, tags map[string]string) bool {
	if len(tokens) == 0 {
		return true
	}
	for k, wantVals := range tokens {
		actual, ok := tags[k]
		if !ok {
			return false
		}
		act := strings.ToLower(strings.TrimSpace(actual))
		matchedOne := false
		for _, want := range wantVals {
			if matchSingleTokenValue(k, act, want) {
				matchedOne = true
				break
			}
		}
		if !matchedOne {
			return false
		}
	}
	return true
}

func matchSingleTokenValue(key, actual, want string) bool {
	switch key {
	case "exit":
		if want == "err" || want == "fail" || want == "failed" || want == "nonzero" {
			return actual != "" && actual != "0"
		}
		if want == "ok" {
			return actual == "0"
		}
	case "secret", "dirty", "online", "enabled", "backedup", "stale":
		if want == "yes" || want == "1" {
			want = "true"
		} else if want == "no" || want == "0" {
			want = "false"
		}
	}
	if actual == want {
		return true
	}
	return strings.HasPrefix(actual, want) || strings.Contains(actual, want)
}
