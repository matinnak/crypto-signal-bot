package dsl

import (
	"math"
	"strings"

	"github.com/matinnak/crypto-signal-bot/internal/indicator"
)

// Data holds the price series a condition is evaluated on, oldest bar first.
type Data struct {
	Open, High, Low, Close, Volume []float64
}

// Len is the number of bars.
func (d *Data) Len() int { return len(d.Close) }

type valueKind int

const (
	kNum valueKind = iota
	kBool
)

func (k valueKind) String() string {
	if k == kBool {
		return "شرط (درست/غلط)"
	}
	return "عدد"
}

// compiled is a type-checked node: its kind plus a function that evaluates it
// for every bar at once. Booleans are encoded as 1 (true) and 0 (false); NaN
// means "not available yet" and is treated as false.
type compiled struct {
	kind valueKind
	eval func(d *Data) []float64
}

// Condition is a compiled boolean expression.
type Condition struct {
	Source string
	c      compiled
}

// Compile parses and type-checks src, which must be a true/false condition.
func Compile(src string) (*Condition, error) {
	n, err := Parse(src)
	if err != nil {
		return nil, err
	}
	c, err := compileNode(n)
	if err != nil {
		return nil, err
	}
	if c.kind != kBool {
		return nil, errAt(n.Pos(), "نتیجه‌ی شرط باید درست/غلط باشد، نه یک عدد. مثلاً: RSI(14) < 30")
	}
	return &Condition{Source: strings.TrimSpace(src), c: c}, nil
}

// Eval returns, for every bar, whether the condition holds.
func (c *Condition) Eval(d *Data) []bool {
	v := c.c.eval(d)
	out := make([]bool, len(v))
	for i, x := range v {
		out[i] = truthy(x)
	}
	return out
}

func truthy(x float64) bool { return !math.IsNaN(x) && x != 0 }

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func constSeries(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

var builtinSeries = map[string]func(d *Data) []float64{
	"open":   func(d *Data) []float64 { return d.Open },
	"high":   func(d *Data) []float64 { return d.High },
	"low":    func(d *Data) []float64 { return d.Low },
	"close":  func(d *Data) []float64 { return d.Close },
	"volume": func(d *Data) []float64 { return d.Volume },
	"hl2": func(d *Data) []float64 {
		return combine(d, func(i int) float64 { return (d.High[i] + d.Low[i]) / 2 })
	},
	"hlc3": func(d *Data) []float64 {
		return combine(d, func(i int) float64 { return (d.High[i] + d.Low[i] + d.Close[i]) / 3 })
	},
	"ohlc4": func(d *Data) []float64 {
		return combine(d, func(i int) float64 { return (d.Open[i] + d.High[i] + d.Low[i] + d.Close[i]) / 4 })
	},
}

func combine(d *Data, f func(i int) float64) []float64 {
	out := make([]float64, d.Len())
	for i := range out {
		out[i] = f(i)
	}
	return out
}

func compileNode(n Node) (compiled, error) {
	switch n := n.(type) {
	case *NumberLit:
		v := n.Value
		return compiled{kNum, func(d *Data) []float64 { return constSeries(d.Len(), v) }}, nil

	case *Ident:
		if f, ok := builtinSeries[n.Name]; ok {
			return compiled{kNum, f}, nil
		}
		if _, ok := functions[n.Name]; ok {
			// "rsi < 30" is accepted as "rsi() < 30" with default settings.
			return compileCall(&Call{At: n.At, Name: n.Name})
		}
		return compiled{}, errAt(n.At, "%q شناخته نشد. برای دیدن لیست اندیکاتورها /indicators را بفرستید", n.Name)

	case *Call:
		return compileCall(n)

	case *Index:
		x, err := compileNode(n.X)
		if err != nil {
			return compiled{}, err
		}
		off := n.Offset
		return compiled{x.kind, func(d *Data) []float64 { return indicator.Shift(x.eval(d), off) }}, nil

	case *Unary:
		x, err := compileNode(n.X)
		if err != nil {
			return compiled{}, err
		}
		if n.Op == "not" {
			if x.kind != kBool {
				return compiled{}, errAt(n.At, "not فقط روی شرط‌ها کار می‌کند")
			}
			return compiled{kBool, func(d *Data) []float64 {
				v := x.eval(d)
				out := make([]float64, len(v))
				for i := range v {
					out[i] = boolf(!truthy(v[i]))
				}
				return out
			}}, nil
		}
		if x.kind != kNum {
			return compiled{}, errAt(n.At, "علامت منفی فقط روی عدد کار می‌کند")
		}
		return compiled{kNum, func(d *Data) []float64 {
			v := x.eval(d)
			out := make([]float64, len(v))
			for i := range v {
				out[i] = -v[i]
			}
			return out
		}}, nil

	case *Binary:
		return compileBinary(n)
	}
	return compiled{}, errAt(n.Pos(), "عبارت پشتیبانی نمی‌شود")
}

func compileBinary(n *Binary) (compiled, error) {
	l, err := compileNode(n.L)
	if err != nil {
		return compiled{}, err
	}
	r, err := compileNode(n.R)
	if err != nil {
		return compiled{}, err
	}

	switch n.Op {
	case "and", "or":
		if l.kind != kBool || r.kind != kBool {
			return compiled{}, errAt(n.At, "دو طرف %s باید شرط باشند (مثلاً RSI(14) < 30)، نه عدد", n.Op)
		}
		isAnd := n.Op == "and"
		return compiled{kBool, func(d *Data) []float64 {
			a, b := l.eval(d), r.eval(d)
			out := make([]float64, len(a))
			for i := range a {
				if isAnd {
					out[i] = boolf(truthy(a[i]) && truthy(b[i]))
				} else {
					out[i] = boolf(truthy(a[i]) || truthy(b[i]))
				}
			}
			return out
		}}, nil
	}

	if l.kind != kNum || r.kind != kNum {
		return compiled{}, errAt(n.At, "دو طرف %s باید عدد باشند", n.Op)
	}

	var op func(a, b float64) float64
	kind := kNum
	switch n.Op {
	case "+":
		op = func(a, b float64) float64 { return a + b }
	case "-":
		op = func(a, b float64) float64 { return a - b }
	case "*":
		op = func(a, b float64) float64 { return a * b }
	case "/":
		op = func(a, b float64) float64 {
			if b == 0 {
				return math.NaN()
			}
			return a / b
		}
	case "<":
		kind, op = kBool, func(a, b float64) float64 { return boolf(a < b) }
	case ">":
		kind, op = kBool, func(a, b float64) float64 { return boolf(a > b) }
	case "<=":
		kind, op = kBool, func(a, b float64) float64 { return boolf(a <= b) }
	case ">=":
		kind, op = kBool, func(a, b float64) float64 { return boolf(a >= b) }
	case "==":
		kind, op = kBool, func(a, b float64) float64 { return boolf(a == b) }
	case "!=":
		kind, op = kBool, func(a, b float64) float64 { return boolf(a != b) }
	default:
		return compiled{}, errAt(n.At, "عملگر ناشناخته %q", n.Op)
	}

	isCmp := kind == kBool
	return compiled{kind, func(d *Data) []float64 {
		a, b := l.eval(d), r.eval(d)
		out := make([]float64, len(a))
		for i := range a {
			if math.IsNaN(a[i]) || math.IsNaN(b[i]) {
				if isCmp {
					out[i] = 0 // comparisons with na are false, like Pine
				} else {
					out[i] = math.NaN()
				}
				continue
			}
			out[i] = op(a[i], b[i])
		}
		return out
	}}, nil
}
