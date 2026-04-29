package searchmod

import "strings"

// stopwords is the default English stop list. Matches RediSearch's
// canonical set so query results line up with the reference engine.
var stopwords = map[string]struct{}{
	"a": {}, "is": {}, "the": {}, "an": {}, "and": {}, "are": {}, "as": {},
	"at": {}, "be": {}, "but": {}, "by": {}, "for": {}, "if": {}, "in": {},
	"into": {}, "it": {}, "no": {}, "not": {}, "of": {}, "on": {}, "or": {},
	"such": {}, "that": {}, "their": {}, "then": {}, "there": {}, "these": {},
	"they": {}, "this": {}, "to": {}, "was": {}, "will": {}, "with": {},
}

// Tokenize splits text on word boundaries, lowercases, and (when stem
// is true) applies a tiny suffix-stripping stemmer. Stop words are
// filtered out unconditionally.
func Tokenize(text string, stem bool) []string {
	if text == "" {
		return nil
	}
	out := make([]string, 0, 16)
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		t := strings.ToLower(cur.String())
		cur.Reset()
		if _, isStop := stopwords[t]; isStop {
			return
		}
		if stem {
			t = stemSuffix(t)
		}
		out = append(out, t)
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		if isWordByte(c) {
			cur.WriteByte(c)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '\'' || c == '_'
}

// stemSuffix is a deliberately tiny suffix stripper — enough to fold
// common English plurals + gerunds without dragging in a full Porter
// implementation. RediSearch uses Snowball; we trade a little recall
// for zero deps.
func stemSuffix(s string) string {
	if len(s) <= 3 {
		return s
	}
	for _, suf := range []string{"ies", "ing", "ed"} {
		if strings.HasSuffix(s, suf) {
			return s[:len(s)-len(suf)]
		}
	}
	if strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") {
		return s[:len(s)-1]
	}
	return s
}

// SplitTags splits a TAG field's raw value by separator and trims/
// lowercases each tag. Empty tags are dropped.
func SplitTags(value, sep string) []string {
	if sep == "" {
		sep = ","
	}
	parts := strings.Split(value, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
