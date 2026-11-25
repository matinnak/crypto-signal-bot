package dsl

import (
	"strings"
	"testing"
)

func data(closes ...float64) *Data {
	d := &Data{}
	for _, c := range closes {
		d.Open = append(d.Open, c)
		d.High = append(d.High, c+1)
		d.Low = append(d.Low, c-1)
		d.Close = append(d.Close, c)
		d.Volume = append(d.Volume, 100)
	}
	return d
}

func TestCompileValid(t *testing.T) {
	valid := []string{
		"RSI(14) < 30 and close > EMA(200)",
		"crossover(EMA(9), EMA(21))",
		"crossunder(MACD_line(12,26,9), MACD_signal(12,26,9))",
		"close < BB_lower(20, 2) and RSI(14) < 35",
		"volume > SMA(volume, 20) * 1.5",
		"close > close[1] and close[1] > close[2]",
		"rsi < 30",
		"not (close > open) or ADX(14, 14) > 25",
		"close >= highest(high, 20)[1]",
		"STOCH_K(14, 3) > STOCH_D(14, 3, 3) && abs(change(close)) > ATR(14)",
		"ema(hl2, 50) > ema(hlc3, 100)",
		"close = open",
	}
	for _, src := range valid {
		if _, err := Compile(src); err != nil {
			t.Errorf("Compile(%q) failed: %v", src, err)
		}
	}
}

func TestCompileErrors(t *testing.T) {
	cases := map[string]string{
		"":                       "خالی",
		"RSI(14)":                "درست/غلط",
		"RSX(14) < 30":           "شناخته نشد",
		"EMA() > 1":              "length",
		"EMA(close, 14.5) > 1":   "عدد صحیح",
		"RSI(14) < 30 and 5":     "and",
		"close > (open":          ")",
		"1 < 2 < 3":              "پشت سر هم",
		"close[x] > 1":           "[ ]",
		"close $ 1":              "نامعتبر",
		"EMA(close, 1, 2) > 1":   "اضافه",
		"crossover(close) == 1":  "ورودی",
		"EMA(close, 9999) > 1":   "بین 1 و",
		"close > open close < 1": "اضافه",
	}
	for src, want := range cases {
		_, err := Compile(src)
		if err == nil {
			t.Errorf("Compile(%q): expected error containing %q", src, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Compile(%q): error %q does not contain %q", src, err, want)
		}
	}
}

func TestEvalComparisonAndHistory(t *testing.T) {
	d := data(1, 2, 3, 2, 5)
	c, err := Compile("close > close[1]")
	if err != nil {
		t.Fatal(err)
	}
	got := c.Eval(d)
	want := []bool{false, true, true, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bar %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestEvalCrossover(t *testing.T) {
	// close crosses above 3 at index 3 and below it at index 5.
	d := data(1, 2, 3, 4, 5, 2)
	over, _ := Compile("crossover(close, 3)")
	under, _ := Compile("crossunder(close, 3)")
	o := over.Eval(d)
	u := under.Eval(d)
	for i := range o {
		if o[i] != (i == 3) {
			t.Errorf("crossover bar %d = %v", i, o[i])
		}
		if u[i] != (i == 5) {
			t.Errorf("crossunder bar %d = %v", i, u[i])
		}
	}
}

func TestEvalLogicAndArithmetic(t *testing.T) {
	d := data(10, 20, 30)
	c, err := Compile("(close * 2 - 10 > 25 and close < 30) or close == 10")
	if err != nil {
		t.Fatal(err)
	}
	got := c.Eval(d)
	want := []bool{true, true, false}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bar %d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestWarmupIsFalse(t *testing.T) {
	d := data(1, 2, 3)
	c, _ := Compile("SMA(close, 5) < 100")
	for i, v := range c.Eval(d) {
		if v {
			t.Errorf("bar %d should be false during warm-up", i)
		}
	}
	n, _ := Compile("not (SMA(close, 5) > 100)")
	// not(na-comparison) is true: comparison with na is false, like Pine.
	if !n.Eval(d)[0] {
		t.Errorf("not(false) should be true")
	}
}

func TestFunctionHelp(t *testing.T) {
	if len(FunctionHelp()) < 15 {
		t.Errorf("expected at least 15 documented functions, got %d", len(FunctionHelp()))
	}
}
