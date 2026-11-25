package dsl

import (
	"strconv"
)

// Grammar (lowest to highest precedence):
//
//	expr    = or
//	or      = and { "or" and }
//	and     = not { "and" not }
//	not     = "not" not | cmp
//	cmp     = add [ ("<"|">"|"<="|">="|"=="|"!=") add ]
//	add     = mul { ("+"|"-") mul }
//	mul     = unary { ("*"|"/") unary }
//	unary   = "-" unary | postfix
//	postfix = primary { "[" NUMBER "]" }
//	primary = NUMBER | IDENT | IDENT "(" [ expr { "," expr } ] ")" | "(" expr ")"

// Node is an AST node.
type Node interface {
	Pos() int
}

// NumberLit is a numeric constant such as 30 or 2.5.
type NumberLit struct {
	At    int
	Value float64
}

// Ident is a built-in series such as close or volume.
type Ident struct {
	At   int
	Name string
}

// Call is a function call such as rsi(14) or crossover(a, b).
type Call struct {
	At   int
	Name string
	Args []Node
}

// Index is a history reference: x[n] is the value of x n bars ago.
type Index struct {
	At     int
	X      Node
	Offset int
}

// Unary is "-x" or "not x".
type Unary struct {
	At int
	Op string
	X  Node
}

// Binary is an arithmetic, comparison or logical operation.
type Binary struct {
	At   int
	Op   string // + - * / < > <= >= == != and or
	L, R Node
}

func (n *NumberLit) Pos() int { return n.At }
func (n *Ident) Pos() int     { return n.At }
func (n *Call) Pos() int      { return n.At }
func (n *Index) Pos() int     { return n.At }
func (n *Unary) Pos() int     { return n.At }
func (n *Binary) Pos() int    { return n.At }

type parser struct {
	toks []token
	i    int
}

// Parse turns source text into an AST without type checking.
func Parse(src string) (Node, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	if p.peek().kind == tEOF {
		return nil, errAt(0, "شرط خالی است")
	}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, errAt(t.pos, "عبارت اضافه %q", t.text)
	}
	return n, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) parseOr() (Node, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tOr {
		t := p.next()
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &Binary{At: t.pos, Op: "or", L: l, R: r}
	}
	return l, nil
}

func (p *parser) parseAnd() (Node, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tAnd {
		t := p.next()
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = &Binary{At: t.pos, Op: "and", L: l, R: r}
	}
	return l, nil
}

func (p *parser) parseNot() (Node, error) {
	if p.peek().kind == tNot {
		t := p.next()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &Unary{At: t.pos, Op: "not", X: x}, nil
	}
	return p.parseCmp()
}

func isCmp(op string) bool {
	switch op {
	case "<", ">", "<=", ">=", "==", "!=":
		return true
	}
	return false
}

func (p *parser) parseCmp() (Node, error) {
	l, err := p.parseAdd()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind == tOp && isCmp(t.text) {
		p.next()
		r, err := p.parseAdd()
		if err != nil {
			return nil, err
		}
		l = &Binary{At: t.pos, Op: t.text, L: l, R: r}
		if t2 := p.peek(); t2.kind == tOp && isCmp(t2.text) {
			return nil, errAt(t2.pos, "دو مقایسه پشت سر هم مجاز نیست؛ از and استفاده کنید (مثلاً a < b and b < c)")
		}
	}
	return l, nil
}

func (p *parser) parseAdd() (Node, error) {
	l, err := p.parseMul()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t.kind == tOp && (t.text == "+" || t.text == "-"); t = p.peek() {
		p.next()
		r, err := p.parseMul()
		if err != nil {
			return nil, err
		}
		l = &Binary{At: t.pos, Op: t.text, L: l, R: r}
	}
	return l, nil
}

func (p *parser) parseMul() (Node, error) {
	l, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t.kind == tOp && (t.text == "*" || t.text == "/"); t = p.peek() {
		p.next()
		r, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		l = &Binary{At: t.pos, Op: t.text, L: l, R: r}
	}
	return l, nil
}

func (p *parser) parseUnary() (Node, error) {
	if t := p.peek(); t.kind == tOp && t.text == "-" {
		p.next()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if num, ok := x.(*NumberLit); ok {
			return &NumberLit{At: t.pos, Value: -num.Value}, nil
		}
		return &Unary{At: t.pos, Op: "-", X: x}, nil
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() (Node, error) {
	x, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tLBrack {
		lb := p.next()
		t := p.next()
		if t.kind != tNumber {
			return nil, errAt(t.pos, "داخل [ ] باید یک عدد صحیح بیاید، مثل close[1]")
		}
		n, err := strconv.Atoi(t.text)
		if err != nil || n < 0 {
			return nil, errAt(t.pos, "داخل [ ] باید یک عدد صحیح مثبت بیاید")
		}
		if rb := p.next(); rb.kind != tRBrack {
			return nil, errAt(rb.pos, "علامت ] جا افتاده")
		}
		x = &Index{At: lb.pos, X: x, Offset: n}
	}
	return x, nil
}

func (p *parser) parsePrimary() (Node, error) {
	t := p.next()
	switch t.kind {
	case tNumber:
		v, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, errAt(t.pos, "عدد نامعتبر %q", t.text)
		}
		return &NumberLit{At: t.pos, Value: v}, nil
	case tIdent:
		if p.peek().kind != tLParen {
			return &Ident{At: t.pos, Name: t.text}, nil
		}
		p.next() // (
		call := &Call{At: t.pos, Name: t.text}
		if p.peek().kind == tRParen {
			p.next()
			return call, nil
		}
		for {
			arg, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			call.Args = append(call.Args, arg)
			sep := p.next()
			if sep.kind == tRParen {
				return call, nil
			}
			if sep.kind != tComma {
				return nil, errAt(sep.pos, "در فراخوانی %s انتظار , یا ) بود", t.text)
			}
		}
	case tLParen:
		x, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if rp := p.next(); rp.kind != tRParen {
			return nil, errAt(rp.pos, "پرانتز ) بسته نشده")
		}
		return x, nil
	case tEOF:
		return nil, errAt(t.pos, "شرط ناقص است")
	default:
		return nil, errAt(t.pos, "انتظار عدد، اندیکاتور یا پرانتز بود اما %q آمد", t.text)
	}
}
