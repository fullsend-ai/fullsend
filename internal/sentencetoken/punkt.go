// Package sentencetoken splits text into sentences using the Punkt algorithm.
//
// Ported from gopkg.in/neurosnap/sentences.v1 (MIT, Copyright 2015 Eric Bower)
// and github.com/jdkato/prose/v2 (MIT, Copyright 2017-2018 Joseph Kato).
// Only the English sentence tokenizer is retained; NER, POS tagging, and all
// interfaces have been removed.
package sentencetoken

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed english.json
var englishJSON []byte

var (
	defaultTokenizer *punktTokenizer
	initOnce         sync.Once
)

type punktTokenizer struct {
	s           *storage
	annotations []annotator
}

func getTokenizer() *punktTokenizer {
	initOnce.Do(func() {
		s, err := loadTraining(englishJSON)
		if err != nil {
			panic("sentencetoken: failed to load english training data: " + err.Error())
		}

		for _, abbr := range []string{"sgt", "gov", "no", "mt"} {
			s.AbbrevTypes[abbr] = 1
		}

		defaultTokenizer = &punktTokenizer{
			s: s,
			annotations: []annotator{
				&typeBasedAnnotation{s},
				&tokenBasedAnnotation{s},
				&multiPunctAnnotation{s},
			},
		}
	})
	return defaultTokenizer
}

func (p *punktTokenizer) tokenize(text string) []string {
	tokens := tokenizeWords(text, true)
	if len(tokens) == 0 {
		return nil
	}

	for _, ann := range p.annotations {
		tokens = ann.annotate(tokens)
	}

	lastBreak := 0
	var sents []string
	for _, tok := range tokens {
		if !tok.SentBreak {
			continue
		}
		sents = append(sents, text[lastBreak:tok.Position])
		lastBreak = tok.Position
	}

	if lastBreak != len(text) {
		sents = append(sents, text[lastBreak:])
	}

	return sents
}

// SplitSentences splits text into sentences using the Punkt algorithm
// with an English language model. Returns at least one element.
func SplitSentences(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{text}
	}

	tok := getTokenizer()
	raw := tok.tokenize(text)

	var result []string
	for _, s := range raw {
		t := strings.TrimSpace(s)
		if t != "" {
			result = append(result, t)
		}
	}

	if len(result) == 0 {
		return []string{text}
	}
	return result
}
