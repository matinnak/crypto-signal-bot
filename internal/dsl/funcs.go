package dsl

import (
	"math"
	"sort"
	"strings"

	ind "github.com/matinnak/crypto-signal-bot/internal/indicator"
)

const maxLength = 500

// param is a constant (numeric literal) argument with an optional default.
type param struct {
	name    string
	def     float64 // NaN = required
	integer bool
}

func p(name string, def float64) param      { return param{name: name, def: def, integer: true} }
func pReq(name string) param                { return param{name: name, def: math.NaN(), integer: true} }
func pFloat(name string, def float64) param { return param{name: name, def: def} }

// funcSpec describes a function callable from conditions.
//
// Arguments are laid out as: [series args...] [optional source] [constant params...].
type funcSpec struct {
	series int  // number of required series (expression) arguments
	source bool // accepts an optional leading source series, defaulting to close
	params []param
	ret    valueKind
	help   string
	fn     func(d *Data, s [][]float64, p []float64) []float64
}

var functions = map[string]*funcSpec{}

func register(names []string, spec *funcSpec) {
	for _, n := range names {
		functions[n] = spec
	}
}

func init() {
	ma := func(f func([]float64, int) []float64) func(*Data, [][]float64, []float64) []float64 {
		return func(_ *Data, s [][]float64, p []float64) []float64 { return f(s[0], int(p[0])) }
	}
	register([]string{"sma"}, &funcSpec{source: true, params: []param{pReq("length")}, help: "SMA(close, 20)", fn: ma(ind.SMA)})
	register([]string{"ema"}, &funcSpec{source: true, params: []param{pReq("length")}, help: "EMA(close, 200)", fn: ma(ind.EMA)})
	register([]string{"rma"}, &funcSpec{source: true, params: []param{pReq("length")}, help: "RMA(close, 14)", fn: ma(ind.RMA)})
	register([]string{"wma"}, &funcSpec{source: true, params: []param{pReq("length")}, help: "WMA(close, 20)", fn: ma(ind.WMA)})
	register([]string{"rsi"}, &funcSpec{source: true, params: []param{p("length", 14)}, help: "RSI(14)", fn: ma(ind.RSI)})

	macdParams := []param{p("fast", 12), p("slow", 26), p("signal", 9)}
	macd := func(pick int) func(*Data, [][]float64, []float64) []float64 {
		return func(_ *Data, s [][]float64, p []float64) []float64 {
			l, sig, h := ind.MACD(s[0], int(p[0]), int(p[1]), int(p[2]))
			return [][]float64{l, sig, h}[pick]
		}
	}
	register([]string{"macd", "macd_line"}, &funcSpec{source: true, params: macdParams, help: "MACD_line(12, 26, 9)", fn: macd(0)})
	register([]string{"macd_signal"}, &funcSpec{source: true, params: macdParams, help: "MACD_signal(12, 26, 9)", fn: macd(1)})
	register([]string{"macd_hist"}, &funcSpec{source: true, params: macdParams, help: "MACD_hist(12, 26, 9)", fn: macd(2)})

	bbParams := []param{p("length", 20), pFloat("mult", 2)}
	bb := func(pick int) func(*Data, [][]float64, []float64) []float64 {
		return func(_ *Data, s [][]float64, p []float64) []float64 {
			m, u, l := ind.Bollinger(s[0], int(p[0]), p[1])
			return [][]float64{m, u, l}[pick]
		}
	}
	register([]string{"bb_mid", "bb_basis", "bb_middle"}, &funcSpec{source: true, params: bbParams, help: "BB_mid(20, 2)", fn: bb(0)})
	register([]string{"bb_upper"}, &funcSpec{source: true, params: bbParams, help: "BB_upper(20, 2)", fn: bb(1)})
	register([]string{"bb_lower"}, &funcSpec{source: true, params: bbParams, help: "BB_lower(20, 2)", fn: bb(2)})

	register([]string{"atr"}, &funcSpec{params: []param{p("length", 14)}, help: "ATR(14)",
		fn: func(d *Data, _ [][]float64, p []float64) []float64 { return ind.ATR(d.High, d.Low, d.Close, int(p[0])) }})

	register([]string{"stoch_k"}, &funcSpec{params: []param{p("length", 14), p("smooth_k", 1)}, help: "STOCH_K(14, 1)",
		fn: func(d *Data, _ [][]float64, p []float64) []float64 {
			k, _ := ind.StochKD(d.Close, d.High, d.Low, int(p[0]), int(p[1]), 1)
			return k
		}})
	register([]string{"stoch_d"}, &funcSpec{params: []param{p("length", 14), p("smooth_k", 1), p("smooth_d", 3)}, help: "STOCH_D(14, 1, 3)",
		fn: func(d *Data, _ [][]float64, p []float64) []float64 {
			_, dd := ind.StochKD(d.Close, d.High, d.Low, int(p[0]), int(p[1]), int(p[2]))
			return dd
		}})

	adx := func(pick int) func(*Data, [][]float64, []float64) []float64 {
		return func(d *Data, _ [][]float64, p []float64) []float64 {
			a, plus, minus := ind.ADX(d.High, d.Low, d.Close, int(p[0]), int(p[1]))
			return [][]float64{a, plus, minus}[pick]
		}
	}
	adxParams := []param{p("di_length", 14), p("adx_smoothing", 14)}
	register([]string{"adx"}, &funcSpec{params: adxParams, help: "ADX(14, 14)", fn: adx(0)})
	register([]string{"plus_di", "di_plus"}, &funcSpec{params: adxParams, help: "PLUS_DI(14)", fn: adx(1)})
	register([]string{"minus_di", "di_minus"}, &funcSpec{params: adxParams, help: "MINUS_DI(14)", fn: adx(2)})

	register([]string{"highest"}, &funcSpec{source: true, params: []param{pReq("length")}, help: "highest(high, 20)", fn: ma(ind.Highest)})
	register([]string{"lowest"}, &funcSpec{source: true, params: []param{pReq("length")}, help: "lowest(low, 20)", fn: ma(ind.Lowest)})
	register([]string{"change"}, &funcSpec{source: true, params: []param{p("bars", 1)}, help: "change(close, 1)",
		fn: func(_ *Data, s [][]float64, p []float64) []float64 {
			prev := ind.Shift(s[0], int(p[0]))
			out := make([]float64, len(prev))
			for i := range out {
				out[i] = s[0][i] - prev[i]
			}
			return out
		}})

	register([]string{"crossover"}, &funcSpec{series: 2, ret: kBool, help: "crossover(EMA(9), EMA(21))",
		fn: func(_ *Data, s [][]float64, _ []float64) []float64 { return cross(s[0], s[1], true, false) }})
	register([]string{"crossunder"}, &funcSpec{series: 2, ret: kBool, help: "crossunder(EMA(9), EMA(21))",
		fn: func(_ *Data, s [][]float64, _ []float64) []float64 { return cross(s[0], s[1], false, true) }})
	register([]string{"cross"}, &funcSpec{series: 2, ret: kBool, help: "cross(close, EMA(50))",
		fn: func(_ *Data, s [][]float64, _ []float64) []float64 { return cross(s[0], s[1], true, true) }})

	register([]string{"abs"}, &funcSpec{series: 1, help: "abs(change(close))",
		fn: func(_ *Data, s [][]float64, _ []float64) []float64 { return mapSeries(s[0], math.Abs) }})
	register([]string{"min"}, &funcSpec{series: 2, help: "min(open, close)",
		fn: func(_ *Data, s [][]float64, _ []float64) []float64 { return zip(s[0], s[1], math.Min) }})
	register([]string{"max"}, &funcSpec{series: 2, help: "max(open, close)",
		fn: func(_ *Data, s [][]float64, _ []float64) []float64 { return zip(s[0], s[1], math.Max) }})
}

func cross(a, b []float64, over, under bool) []float64 {
	out := make([]float64, len(a))
	for i := 1; i < len(a); i++ {
		up := a[i] > b[i] && a[i-1] <= b[i-1]
		down := a[i] < b[i] && a[i-1] >= b[i-1]
		out[i] = boolf(over && up || under && down)
	}
	return out
}

func mapSeries(a []float64, f func(float64) float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = f(a[i])
	}
	return out
}

func zip(a, b []float64, f func(x, y float64) float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		if math.IsNaN(a[i]) || math.IsNaN(b[i]) {
			out[i] = math.NaN()
			continue
		}
		out[i] = f(a[i], b[i])
	}
	return out
}

func compileCall(c *Call) (compiled, error) {
	spec, ok := functions[c.Name]
	if !ok {
		return compiled{}, errAt(c.At, "اندیکاتور %q شناخته نشد. برای دیدن لیست /indicators را بفرستید", c.Name)
	}
	args := c.Args
	usage := " (نمونه: " + spec.help + ")"

	// 1. Required series arguments.
	if len(args) < spec.series {
		return compiled{}, errAt(c.At, "%s به %d ورودی نیاز دارد%s", c.Name, spec.series, usage)
	}
	var seriesArgs []compiled
	for _, a := range args[:spec.series] {
		ca, err := compileNode(a)
		if err != nil {
			return compiled{}, err
		}
		if ca.kind != kNum {
			return compiled{}, errAt(a.Pos(), "ورودی %s باید عدد باشد، نه شرط", c.Name)
		}
		seriesArgs = append(seriesArgs, ca)
	}
	args = args[spec.series:]

	// 2. Optional source: the first remaining argument if it is not a literal.
	if spec.source {
		src := compiled{kNum, builtinSeries["close"]}
		if len(args) > 0 {
			if _, isNum := args[0].(*NumberLit); !isNum {
				ca, err := compileNode(args[0])
				if err != nil {
					return compiled{}, err
				}
				if ca.kind != kNum {
					return compiled{}, errAt(args[0].Pos(), "منبع %s باید عدد باشد (مثل close)", c.Name)
				}
				src = ca
				args = args[1:]
			}
		}
		seriesArgs = append(seriesArgs, src)
	}

	// 3. Constant parameters.
	if len(args) > len(spec.params) {
		return compiled{}, errAt(c.At, "%s ورودی اضافه دارد%s", c.Name, usage)
	}
	vals := make([]float64, len(spec.params))
	for i, prm := range spec.params {
		if i >= len(args) {
			if math.IsNaN(prm.def) {
				return compiled{}, errAt(c.At, "%s به %s نیاز دارد%s", c.Name, prm.name, usage)
			}
			vals[i] = prm.def
			continue
		}
		lit, ok := args[i].(*NumberLit)
		if !ok {
			return compiled{}, errAt(args[i].Pos(), "%s در %s باید یک عدد ثابت باشد%s", prm.name, c.Name, usage)
		}
		v := lit.Value
		if prm.integer {
			if v != math.Trunc(v) || v < 1 || v > maxLength {
				return compiled{}, errAt(lit.At, "%s در %s باید عدد صحیح بین 1 و %d باشد", prm.name, c.Name, maxLength)
			}
		} else if v <= 0 {
			return compiled{}, errAt(lit.At, "%s در %s باید بزرگ‌تر از صفر باشد", prm.name, c.Name)
		}
		vals[i] = v
	}

	fn := spec.fn
	return compiled{spec.ret, func(d *Data) []float64 {
		s := make([][]float64, len(seriesArgs))
		for i, a := range seriesArgs {
			s[i] = a.eval(d)
		}
		return fn(d, s, vals)
	}}, nil
}

// FunctionHelp returns one usage example per distinct function, sorted.
func FunctionHelp() []string {
	seen := map[*funcSpec]bool{}
	var out []string
	for _, spec := range functions {
		if seen[spec] {
			continue
		}
		seen[spec] = true
		out = append(out, spec.help)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}
