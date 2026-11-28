package fuzzy

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
)

func init() {
	algo.Init("default")
}

var slabPool = sync.Pool{
	New: func() any {
		return util.MakeSlab(100*1024, 2048)
	},
}

const bonusFullMatch = 24

type Scheme int

const (
	SchemeDefault Scheme = iota
	SchemePath
)

type TokenKind int

const (
	TokenFuzzy TokenKind = iota
	TokenExact
	TokenPrefix
	TokenSuffix
	TokenEqual
)

type Token struct {
	Raw           string
	Pattern       string
	Kind          TokenKind
	Negate        bool
	CaseSensitive bool
}

type Result struct {
	Matched   bool
	Score     int
	Start     int
	End       int
	Positions []int
}

func ParseQuery(raw string) []Token {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return nil
	}
	tokens := make([]Token, 0, len(fields))
	for _, f := range fields {
		tok := Token{Raw: f}
		if strings.HasPrefix(f, "!") && len(f) > 1 {
			tok.Negate = true
			f = f[1:]
		}
		switch {
		case strings.HasPrefix(f, "'"):
			tok.Kind = TokenExact
			tok.Pattern = f[1:]
		case len(f) > 2 && strings.HasPrefix(f, "^") && strings.HasSuffix(f, "$"):
			tok.Kind = TokenEqual
			tok.Pattern = f[1 : len(f)-1]
		case strings.HasPrefix(f, "^"):
			tok.Kind = TokenPrefix
			tok.Pattern = f[1:]
		case strings.HasSuffix(f, "$"):
			tok.Kind = TokenSuffix
			tok.Pattern = f[:len(f)-1]
		default:
			tok.Kind = TokenFuzzy
			tok.Pattern = f
		}
		if tok.Pattern == "" {
			continue
		}
		tok.CaseSensitive = containsUpper(tok.Pattern)
		tokens = append(tokens, tok)
	}
	return tokens
}

func Match(text, pattern string, scheme Scheme, wantPos bool) Result {
	return MatchToken(text, Token{
		Raw:           pattern,
		Pattern:       pattern,
		Kind:          TokenFuzzy,
		CaseSensitive: containsUpper(pattern),
	}, scheme, wantPos)
}

func MatchToken(text string, tok Token, scheme Scheme, wantPos bool) Result {
	if tok.Pattern == "" {
		return Result{Matched: true}
	}
	chars := util.ToChars([]byte(text))
	pat := tok.Pattern
	if !tok.CaseSensitive {
		pat = strings.ToLower(pat)
	}
	patRunes := []rune(pat)
	if len(patRunes) > chars.Length() {
		return Result{}
	}

	forward := scheme != SchemePath
	slab := slabPool.Get().(*util.Slab)
	defer slabPool.Put(slab)

	var res algo.Result
	var pos *[]int
	switch tok.Kind {
	case TokenEqual:
		res, pos = algo.EqualMatch(tok.CaseSensitive, false, forward, &chars, patRunes, wantPos, slab)
	case TokenPrefix:
		res, pos = algo.PrefixMatch(tok.CaseSensitive, false, forward, &chars, patRunes, wantPos, slab)
	case TokenSuffix:
		res, pos = algo.SuffixMatch(tok.CaseSensitive, false, forward, &chars, patRunes, wantPos, slab)
	case TokenExact:
		res, pos = algo.ExactMatchNaive(tok.CaseSensitive, false, forward, &chars, patRunes, wantPos, slab)
	default:
		res, pos = algo.FuzzyMatchV2(tok.CaseSensitive, false, forward, &chars, patRunes, wantPos, slab)
	}
	if res.Start < 0 {
		return Result{}
	}

	score := res.Score
	if res.Start == 0 && res.End == chars.Length() {
		score += bonusFullMatch
	}

	start := res.Start
	end := res.End - 1
	if end < start {
		end = start
	}
	var positions []int
	if pos != nil && len(*pos) > 0 {
		positions = *pos
		if len(positions) > 1 {
			sort.Ints(positions)
		}
		start = positions[0]
		end = positions[len(positions)-1]
	} else if wantPos && res.End > res.Start {
		positions = make([]int, res.End-res.Start)
		for i := range positions {
			positions[i] = res.Start + i
		}
	}

	return Result{
		Matched:   true,
		Score:     score,
		Start:     start,
		End:       end,
		Positions: positions,
	}
}

func MatchPositions(text string, tokens []Token, scheme Scheme) []int {
	if text == "" || len(tokens) == 0 {
		return nil
	}
	var pos []int
	var seen map[int]bool
	for _, tok := range tokens {
		if tok.Negate || tok.Pattern == "" {
			continue
		}
		res := MatchToken(text, tok, scheme, true)
		if !res.Matched || len(res.Positions) == 0 {
			continue
		}
		if seen == nil {
			seen = make(map[int]bool, len(res.Positions)*len(tokens))
		}
		for _, p := range res.Positions {
			if !seen[p] {
				seen[p] = true
				pos = append(pos, p)
			}
		}
	}
	if len(pos) > 1 {
		sort.Ints(pos)
	}
	return pos
}

func containsUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
