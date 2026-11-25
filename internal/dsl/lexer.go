// Package dsl implements a tiny, Pine-like condition language used to define
// strategies from Telegram, for example:
//
//	RSI(14) < 30 and close > EMA(200)
//	crossover(EMA(9), EMA(21))
//
// Source text is tokenized (lexer.go), parsed into an AST (parser.go),
// type-checked and evaluated bar by bar over candle data (eval.go, funcs.go).
package dsl

import (
	"fmt"
	"strings"
	"unicode"
)

type tokenKind int

const (
	tEOF tokenKind = iota
	tNumber
	tIdent
	tLParen
	tRParen
	tLBrack
	tRBrack
	tComma
	tOp  // + - * / < > <= >= == !=
	tAnd // and, &&
	tOr  // or, ||
	tNot // not, !
)

type token struct {
	kind tokenKind
	text string
	pos  int // byte offset, for error messages
}

// Error is a syntax or type error with a position in the source text.
type Error struct {
	Pos int
	Msg string
}

func (e *Error) Error() string {
	return fmt.Sprintf("خطا در کاراکتر %d: %s", e.Pos+1, e.Msg)
}

func errAt(pos int, format string, args ...any) *Error {
	return &Error{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

func lex(src string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c >= '0' && c <= '9' || c == '.':
			start := i
			for i < len(src) && (src[i] >= '0' && src[i] <= '9' || src[i] == '.') {
				i++
			}
			toks = append(toks, token{tNumber, src[start:i], start})
		case c == '_' || unicode.IsLetter(rune(c)) && c < 128:
			start := i
			for i < len(src) && (src[i] == '_' || src[i] < 128 && (unicode.IsLetter(rune(src[i])) || unicode.IsDigit(rune(src[i])))) {
				i++
			}
			word := src[start:i]
			switch strings.ToLower(word) {
			case "and":
				toks = append(toks, token{tAnd, word, start})
			case "or":
				toks = append(toks, token{tOr, word, start})
			case "not":
				toks = append(toks, token{tNot, word, start})
			default:
				toks = append(toks, token{tIdent, strings.ToLower(word), start})
			}
		case c == '(':
			toks = append(toks, token{tLParen, "(", i})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")", i})
			i++
		case c == '[':
			toks = append(toks, token{tLBrack, "[", i})
			i++
		case c == ']':
			toks = append(toks, token{tRBrack, "]", i})
			i++
		case c == ',':
			toks = append(toks, token{tComma, ",", i})
			i++
		case c == '&' && i+1 < len(src) && src[i+1] == '&':
			toks = append(toks, token{tAnd, "&&", i})
			i += 2
		case c == '|' && i+1 < len(src) && src[i+1] == '|':
			toks = append(toks, token{tOr, "||", i})
			i += 2
		case c == '<' || c == '>' || c == '=' || c == '!':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tOp, src[i : i+2], i})
				i += 2
				continue
			}
			switch c {
			case '=':
				// Accept a single "=" as equality, like many beginners write.
				toks = append(toks, token{tOp, "==", i})
			case '!':
				toks = append(toks, token{tNot, "!", i})
			default:
				toks = append(toks, token{tOp, string(c), i})
			}
			i++
		case c == '+' || c == '-' || c == '*' || c == '/':
			toks = append(toks, token{tOp, string(c), i})
			i++
		default:
			r := []rune(src[i:])[0]
			return nil, errAt(i, "کاراکتر نامعتبر %q", r)
		}
	}
	toks = append(toks, token{tEOF, "", len(src)})
	return toks, nil
}
