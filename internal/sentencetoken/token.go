// Ported from gopkg.in/neurosnap/sentences.v1 (MIT, Copyright 2015 Eric Bower)
// and github.com/jdkato/prose/v2 (MIT, Copyright 2017-2018 Joseph Kato).
// Flattened into a single internal package; only English sentence splitting retained.

package sentencetoken

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	reEllipsis = regexp.MustCompile(`\.\.+$`)
	reNumeric  = regexp.MustCompile(`-?[\.,]?\d[\d,\.-]*\.?$`)
	reInitial  = regexp.MustCompile(`^[A-Za-z]\.$`)
)

type token struct {
	Tok       string
	Position  int
	SentBreak bool
	ParaStart bool
	LineStart bool
	Abbr      bool
}

func newToken(tok string) *token {
	return &token{Tok: tok}
}

type setString map[string]int

func (ss setString) has(str string) bool {
	return ss[str] != 0
}

type storage struct {
	AbbrevTypes  setString `json:"AbbrevTypes"`
	Collocations setString `json:"Collocations"`
	SentStarters setString `json:"SentStarters"`
	OrthoContext setString `json:"OrthoContext"`
}

func loadTraining(data []byte) (*storage, error) {
	var s storage
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *storage) isAbbr(tokens ...string) bool {
	for _, t := range tokens {
		if s.AbbrevTypes.has(t) {
			return true
		}
	}
	return false
}

// Orthographic context constants.
const (
	orthoBegUc = 1 << 1
	orthoMidUc = 1 << 2
	orthoUnkUc = 1 << 3
	orthoBegLc = 1 << 4
	orthoMidLc = 1 << 5
	orthoUnkLc = 1 << 6
	orthoUc    = orthoBegUc + orthoMidUc + orthoUnkUc
	orthoLc    = orthoBegLc + orthoMidLc + orthoUnkLc
)

func orthoHeuristic(s *storage, tok *token) int {
	if tok == nil {
		return 0
	}

	for _, punct := range ";:,.!?" {
		if tok.Tok == string(punct) {
			return 0
		}
	}

	typ := typeNoSentPeriod(tok)
	orthoCtx := s.OrthoContext[typ]

	if firstUpper(tok) && (orthoCtx&orthoLc > 0 && orthoCtx&orthoMidUc == 0) {
		return 1
	}

	if firstLower(tok) && (orthoCtx&orthoUc > 0 || orthoCtx&orthoBegLc == 0) {
		return 0
	}

	return -1
}

// Punctuation helpers.

func hasSentencePunct(text string) bool {
	for _, c := range text {
		if c == '.' || c == '!' || c == '?' {
			return true
		}
	}
	return false
}

// Token type helpers — flattened from WordTokenizer + TokenParser interfaces.

func tokenType(t *token) string {
	typ := reNumeric.ReplaceAllString(strings.ToLower(t.Tok), "##number##")
	if utf8.RuneCountInString(typ) == 1 {
		return typ
	}
	return strings.Replace(typ, ",", "", -1)
}

func typeNoPeriod(t *token) string {
	typ := tokenType(t)
	if utf8.RuneCountInString(typ) > 1 && typ[len(typ)-1] == '.' {
		return typ[:len(typ)-1]
	}
	return typ
}

func typeNoSentPeriod(t *token) string {
	if t.SentBreak {
		return typeNoPeriod(t)
	}
	return tokenType(t)
}

func firstUpper(t *token) bool {
	if t.Tok == "" {
		return false
	}
	return unicode.IsUpper([]rune(t.Tok)[0])
}

func firstLower(t *token) bool {
	if t.Tok == "" {
		return false
	}
	return unicode.IsLower([]rune(t.Tok)[0])
}

func isEllipsis(t *token) bool    { return reEllipsis.MatchString(t.Tok) }
func isInitial(t *token) bool     { return reInitial.MatchString(t.Tok) }
func hasPeriodFinal(t *token) bool { return strings.HasSuffix(t.Tok, ".") }

// hasSentEndChars — prose's customized version that excludes entities like "Yahoo!".
var reEntities = regexp.MustCompile(`Yahoo!`)

func hasSentEndChars(t *token) bool {
	enders := []string{
		`."`, `.)`, `.'`, `."`,
		`?`, `?"`, `?'`, `?)`, `?'`, `?"`,
		`!`, `!"`, `!'`, `!)`, `!'`, `!"`,
	}
	for _, e := range enders {
		if strings.HasSuffix(t.Tok, e) && !reEntities.MatchString(t.Tok) {
			return true
		}
	}

	parens := []string{
		`.[`, `.(`, `."`,
		`?[`, `?(`,
		`![`, `!(`,
	}
	for _, p := range parens {
		if strings.Contains(t.Tok, p) {
			return true
		}
	}

	return false
}

// Token grouping — pairs adjacent tokens for annotation passes.

func groupTokens(tokens []*token) [][2]*token {
	if len(tokens) == 0 {
		return nil
	}

	pairs := make([][2]*token, 0, len(tokens))
	prev := tokens[0]
	for _, tok := range tokens[1:] {
		pairs = append(pairs, [2]*token{prev, tok})
		prev = tok
	}
	pairs = append(pairs, [2]*token{prev, nil})
	return pairs
}
