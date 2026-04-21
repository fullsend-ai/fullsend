package sentencetoken

import (
	"regexp"
	"strings"
)

// annotator modifies token properties during sentence boundary detection.
type annotator interface {
	annotate([]*token) []*token
}

// typeBasedAnnotation performs the first pass: marks sentence breaks,
// ellipsis, and abbreviations based on token type alone.
type typeBasedAnnotation struct {
	s *storage
}

func (a *typeBasedAnnotation) annotate(tokens []*token) []*token {
	for _, tok := range tokens {
		a.typeAnnotation(tok)
	}
	return tokens
}

func (a *typeBasedAnnotation) typeAnnotation(tok *token) {
	if hasSentEndChars(tok) {
		tok.SentBreak = true
	} else if hasPeriodFinal(tok) && !strings.HasSuffix(tok.Tok, "..") {
		tokNoPeriod := strings.ToLower(tok.Tok[:len(tok.Tok)-1])
		parts := strings.Split(tokNoPeriod, "-")
		lastPart := parts[len(parts)-1]

		if a.s.isAbbr(tokNoPeriod, lastPart) {
			tok.Abbr = true
		} else {
			tok.SentBreak = true
		}
	}
}

// tokenBasedAnnotation performs the second pass: uses collocation,
// orthographic, and sentence-starter heuristics to refine boundaries.
type tokenBasedAnnotation struct {
	s *storage
}

func (a *tokenBasedAnnotation) annotate(tokens []*token) []*token {
	for _, pair := range groupTokens(tokens) {
		a.tokenAnnotation(pair[0], pair[1])
	}
	return tokens
}

func (a *tokenBasedAnnotation) tokenAnnotation(tokOne, tokTwo *token) {
	if tokTwo == nil {
		return
	}

	if !hasPeriodFinal(tokOne) {
		return
	}

	typ := typeNoPeriod(tokOne)
	nextTyp := typeNoSentPeriod(tokTwo)
	tokIsInitial := isInitial(tokOne)

	// [4.1.2] Collocation heuristic.
	collocation := typ + "," + nextTyp
	if a.s.Collocations[collocation] != 0 {
		tokOne.SentBreak = false
		tokOne.Abbr = true
		return
	}

	// [4.2] Token-based reclassification of abbreviations.
	if (tokOne.Abbr || isEllipsis(tokOne)) && !tokIsInitial {
		// [4.1.1] Orthographic heuristic.
		if orthoHeuristic(a.s, tokTwo) == 1 {
			tokOne.SentBreak = true
			return
		}

		// [4.1.3] Frequent sentence starter heuristic.
		if firstUpper(tokTwo) && a.s.SentStarters[nextTyp] != 0 {
			tokOne.SentBreak = true
			return
		}
	}

	// Spaced ellipsis ". . ."
	if tokOne.Tok == "." && tokTwo.Tok == "." {
		tokOne.SentBreak = false
		tokTwo.SentBreak = false
		return
	}

	// [4.3] Token-based detection of initials and ordinals.
	if tokIsInitial || typ == "##number##" {
		isSentStarter := orthoHeuristic(a.s, tokTwo)

		if isSentStarter == 0 {
			tokOne.SentBreak = false
			tokOne.Abbr = true
			return
		}

		if isSentStarter == -1 && tokIsInitial && firstUpper(tokTwo) &&
			a.s.OrthoContext[nextTyp]&orthoLc == 0 {
			tokOne.SentBreak = false
			tokOne.Abbr = true
			return
		}
	}
}

// multiPunctAnnotation is prose's custom third pass that handles
// multi-period abbreviations like "F.B.I.", errant newlines, ellipsis
// patterns, and sentence-internal punctuation.
type multiPunctAnnotation struct {
	s *storage
}

var reAbbr = regexp.MustCompile(`((?:[\w]\.)+[\w]*\.)`)
var reLooksLikeEllipsis = regexp.MustCompile(`(?:\.\s?){2,}\.`)

func (a *multiPunctAnnotation) annotate(tokens []*token) []*token {
	for _, pair := range groupTokens(tokens) {
		if pair[1] == nil {
			tok := pair[0].Tok
			if strings.Contains(tok, "\n") && strings.Contains(tok, " ") {
				pair[0].SentBreak = false
			}
			continue
		}
		a.tokenAnnotation(pair[0], pair[1])
	}
	return tokens
}

func looksInternal(tok string) bool {
	for _, p := range []string{")", `'`, `"`, "\u201d", "\u2019"} {
		if strings.HasSuffix(tok, p) {
			return true
		}
	}
	return false
}

func (a *multiPunctAnnotation) tokenAnnotation(tokOne, tokTwo *token) {
	var nextTyp string

	if strings.HasSuffix(tokOne.Tok, ".") && tokTwo.Tok == "." {
		tokOne.SentBreak = false
		tokTwo.SentBreak = false
		return
	}

	isNonBreak := strings.HasSuffix(tokOne.Tok, ".") && !tokOne.SentBreak
	isEllip := reLooksLikeEllipsis.MatchString(tokOne.Tok)
	isInt := tokOne.SentBreak && looksInternal(tokOne.Tok)

	if isNonBreak || isEllip || isInt {
		nextTyp = typeNoSentPeriod(tokTwo)
		isStarter := a.s.SentStarters[nextTyp]

		if isEllip {
			if firstUpper(tokTwo) || isStarter != 0 {
				tokOne.SentBreak = true
				return
			}
		}

		if isInt {
			if firstLower(tokTwo) && isStarter == 0 {
				tokOne.SentBreak = false
				return
			}
		}

		if isNonBreak && firstUpper(tokTwo) {
			if a.s.OrthoContext[nextTyp]&orthoLc != 0 {
				tokOne.SentBreak = true
			}
		}
	}

	if len(reAbbr.FindAllString(tokOne.Tok, 1)) == 0 {
		return
	}

	if isInitial(tokOne) {
		return
	}

	tokOne.Abbr = true
	tokOne.SentBreak = false

	if orthoHeuristic(a.s, tokTwo) == 1 {
		tokOne.SentBreak = true
		return
	}

	if nextTyp == "" {
		nextTyp = typeNoSentPeriod(tokTwo)
	}

	if firstUpper(tokTwo) && a.s.SentStarters[nextTyp] != 0 {
		tokOne.SentBreak = true
		return
	}
}
