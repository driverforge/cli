package initdriver

import (
	"bytes"
	"strings"
)

// word is an identifier token with its byte span in the source.
type word struct {
	text       string
	start, end int
}

// longSpans returns the byte ranges [start,end) of Lua's multi-line-capable
// literals — long-bracket strings and long comments ([[…]], [=[…]=], --[[…]]).
// reindent uses it to avoid prepending indentation inside a string literal,
// which would silently change its runtime value.
func longSpans(src []byte) [][2]int {
	var spans [][2]int
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		// Long comment --[[ … ]] (skip; its interior must not be reindented).
		if c == '-' && i+1 < n && src[i+1] == '-' {
			j := i + 2
			if _, after, ok := longBracket(src, j); ok {
				end := skipLong(src, after, longLevel(src, j))
				spans = append(spans, [2]int{i, end})
				i = end
				continue
			}
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// Long string [[ … ]].
		if c == '[' {
			if lvl, after, ok := longBracket(src, i); ok {
				end := skipLong(src, after, lvl)
				spans = append(spans, [2]int{i, end})
				i = end
				continue
			}
		}
		// Short strings can't span lines, but skip them so a bracket inside
		// one isn't misread as opening a long string.
		if c == '"' || c == '\'' {
			i = skipShort(src, i, c)
			continue
		}
		i++
	}
	return spans
}

// stripComments returns a copy of src with every comment (line and long)
// replaced by spaces, newlines preserved. String literals are left intact: the
// presence checks it feeds — alreadyPatched, ensureConfigRequire — legitimately
// match a *string argument* (require('vendor.anvil-sdk'), require('config')), so
// blanking strings would hide the real construct (and, for alreadyPatched, make
// an already-wired driver look unpatched and get double-patched). Only a
// commented-out copy should stop counting. Byte offsets/line numbers are
// unchanged, so a match still maps back to src.
func stripComments(src []byte) []byte { return maskLua(src, false) }

// CodeOnly returns a copy of src with comments AND string literals replaced by
// spaces (newlines preserved). Use it for checks that only ever match code — a
// logger call (Log:Info(…)) never lives inside a string — so a name appearing
// only in a comment or a string doesn't trigger a false positive.
func CodeOnly(src []byte) []byte { return maskLua(src, true) }

// maskLua blanks comment spans (and, when maskStrings, string-literal spans)
// with spaces, preserving newlines and byte offsets. It reuses the same
// longBracket/skipLong/skipShort primitives as luaWords, so its notion of what
// is a comment or a string is identical. Strings are always *skipped* (so a --
// or ]] inside one isn't misread) even when they aren't blanked.
func maskLua(src []byte, maskStrings bool) []byte {
	out := make([]byte, len(src))
	copy(out, src)
	blank := func(start, end int) {
		for k := start; k < end; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	n := len(src)
	for i := 0; i < n; {
		c := src[i]

		// Comment: -- line, or --[[ long ]].
		if c == '-' && i+1 < n && src[i+1] == '-' {
			start := i
			j := i + 2
			if _, after, ok := longBracket(src, j); ok {
				end := skipLong(src, after, longLevel(src, j))
				blank(start, end)
				i = end
				continue
			}
			for i < n && src[i] != '\n' {
				i++
			}
			blank(start, i)
			continue
		}

		// Long string [[ ... ]] / [=[ ... ]=].
		if c == '[' {
			if lvl, after, ok := longBracket(src, i); ok {
				end := skipLong(src, after, lvl)
				if maskStrings {
					blank(i, end)
				}
				i = end
				continue
			}
		}

		// Short string.
		if c == '"' || c == '\'' {
			end := skipShort(src, i, c)
			if maskStrings {
				blank(i, end)
			}
			i = end
			continue
		}

		i++
	}
	return out
}

// luaWords tokenises Lua source into identifier words, skipping the contents of
// comments and string literals so keywords inside them are never miscounted. It
// is intentionally minimal — it recognises only what block-matching needs:
// identifiers, line/long comments, and short/long strings.
func luaWords(src []byte) []word {
	var words []word
	n := len(src)
	for i := 0; i < n; {
		c := src[i]

		// Comments: -- line, or --[[ long ]].
		if c == '-' && i+1 < n && src[i+1] == '-' {
			i += 2
			if _, after, ok := longBracket(src, i); ok {
				i = skipLong(src, after, longLevel(src, i))
				continue
			}
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}

		// Long string [[ ... ]] / [=[ ... ]=].
		if c == '[' {
			if lvl, after, ok := longBracket(src, i); ok {
				i = skipLong(src, after, lvl)
				continue
			}
		}

		// Short string.
		if c == '"' || c == '\'' {
			i = skipShort(src, i, c)
			continue
		}

		// Identifier / keyword.
		if isIdentStart(c) {
			j := i + 1
			for j < n && isIdentPart(src[j]) {
				j++
			}
			words = append(words, word{text: string(src[i:j]), start: i, end: j})
			i = j
			continue
		}

		i++
	}
	return words
}

// longBracket reports whether src[i:] opens a long bracket ([[, [=[, ...). It
// returns the level (number of '=') and the index just past the opening bracket.
func longBracket(src []byte, i int) (level, after int, ok bool) {
	if i >= len(src) || src[i] != '[' {
		return 0, 0, false
	}
	j := i + 1
	for j < len(src) && src[j] == '=' {
		level++
		j++
	}
	if j < len(src) && src[j] == '[' {
		return level, j + 1, true
	}
	return 0, 0, false
}

// longLevel returns just the level of a long bracket known to open at i.
func longLevel(src []byte, i int) int {
	lvl, _, _ := longBracket(src, i)
	return lvl
}

// skipLong returns the index just past the closing long bracket of the given
// level, or len(src) if the literal is unterminated.
func skipLong(src []byte, after, level int) int {
	closer := "]" + strings.Repeat("=", level) + "]"
	idx := bytes.Index(src[after:], []byte(closer))
	if idx < 0 {
		return len(src)
	}
	return after + idx + len(closer)
}

// skipShort returns the index just past the closing quote of a short string
// starting at i (the opening quote), honouring backslash escapes.
func skipShort(src []byte, i int, q byte) int {
	i++ // past the opening quote
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		case '\n':
			return i + 1 // unterminated — bail at the line end
		}
		i++
	}
	return len(src)
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// matchingEnd finds the byte span of the `end` that closes a block which is
// already open (depth 1) at fromOffset. Block-opening keywords are function/
// if/do (for/while are closed by their `do`; repeat/until carry no `end`). It
// returns the span of the closing `end` and ok=false if none balances.
func matchingEnd(src []byte, fromOffset int) (endStart, endEnd int, ok bool) {
	depth := 1
	for _, w := range luaWords(src) {
		if w.start < fromOffset {
			continue
		}
		switch w.text {
		case "function", "if", "do":
			depth++
		case "end":
			depth--
			if depth == 0 {
				return w.start, w.end, true
			}
		}
	}
	return 0, 0, false
}
