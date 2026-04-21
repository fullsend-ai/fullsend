package sentencetoken

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenizeWords splits text into tokens, preserving character positions.
// When onlyPeriodContext is true, only tokens near sentence-ending punctuation
// are kept (the word before and after).
func tokenizeWords(text string, onlyPeriodContext bool) []*token {
	textLen := len(text)
	if textLen == 0 {
		return nil
	}

	tokens := make([]*token, 0, 50)
	lastSpace := 0
	lineStart := false
	paragraphStart := false
	getNextWord := false

	for i, char := range text {
		if !unicode.IsSpace(char) && i+utf8.RuneLen(char) != textLen {
			continue
		}

		if char == '\n' {
			if lineStart {
				paragraphStart = true
			}
			lineStart = true
		}

		var cursor int
		if i+utf8.RuneLen(char) == textLen && !unicode.IsSpace(char) {
			cursor = textLen
		} else {
			cursor = i
		}

		word := strings.TrimSpace(text[lastSpace:cursor])
		if word == "" {
			continue
		}

		hasPunct := hasSentencePunct(word)
		if onlyPeriodContext && !hasPunct && !getNextWord {
			lastSpace = cursor
			continue
		}

		tok := newToken(word)
		tok.Position = cursor
		tok.ParaStart = paragraphStart
		tok.LineStart = lineStart
		tokens = append(tokens, tok)

		lastSpace = cursor
		lineStart = false
		paragraphStart = false

		if hasPunct {
			getNextWord = true
		} else {
			getNextWord = false
		}
	}

	if len(tokens) == 0 {
		tok := newToken(text)
		tok.Position = textLen
		tokens = append(tokens, tok)
	}

	return tokens
}
