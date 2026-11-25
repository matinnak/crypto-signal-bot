package indicator

import (
	"math"
	"testing"
)

func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.IsNaN(want) {
		if !math.IsNaN(got) {
			t.Errorf("%s: got %v, want NaN", name, got)
		}
		return
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func TestSMA(t *testing.T) {
	got := SMA([]float64{1, 2, 3, 4, 5}, 3)
	want := []float64{NaN, NaN, 2, 3, 4}
	for i := range want {
		approx(t, "sma", got[i], want[i])
	}
}

func TestEMASeededWithSMA(t *testing.T) {
	src := []float64{1, 2, 3, 4, 5}
	got := EMA(src, 3)
	// seed = sma(1,2,3) = 2; alpha = 0.5
	approx(t, "ema[1]", got[1], NaN)
	approx(t, "ema[2]", got[2], 2)
	approx(t, "ema[3]", got[3], 0.5*4+0.5*2)
	approx(t, "ema[4]", got[4], 0.5*5+0.5*3)
}

func TestRMA(t *testing.T) {
	got := RMA([]float64{2, 4, 6, 8}, 2)
	// seed = 3 at index 1; alpha = 0.5
	approx(t, "rma[1]", got[1], 3)
	approx(t, "rma[2]", got[2], 4.5)
	approx(t, "rma[3]", got[3], 6.25)
}

func TestRSIExtremes(t *testing.T) {
	up := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	down := []float64{8, 7, 6, 5, 4, 3, 2, 1}
	r := RSI(up, 3)
	approx(t, "rsi up warmup", r[2], NaN)
	approx(t, "rsi up", r[7], 100)
	approx(t, "rsi down", RSI(down, 3)[7], 0)
}

func TestRSIKnownValue(t *testing.T) {
	// Changes after seed: up=[1,0], down=[0,1] with length 2.
	src := []float64{10, 11, 10}
	r := RSI(src, 2)
	// rma(up) seed = (1+0)/2 = 0.5 ; rma(down) seed = (0+1)/2 = 0.5 -> RSI 50
	approx(t, "rsi", r[2], 50)
}

func TestBollingerConstant(t *testing.T) {
	src := []float64{5, 5, 5, 5}
	m, u, l := Bollinger(src, 3, 2)
	approx(t, "mid", m[3], 5)
	approx(t, "upper", u[3], 5)
	approx(t, "lower", l[3], 5)
}

func TestStdevPopulation(t *testing.T) {
	got := Stdev([]float64{2, 4, 4, 4, 5, 5, 7, 9}, 8)
	approx(t, "stdev", got[7], 2)
}

func TestHighestLowest(t *testing.T) {
	src := []float64{3, 1, 4, 1, 5}
	h := Highest(src, 3)
	l := Lowest(src, 3)
	approx(t, "highest", h[4], 5)
	approx(t, "highest", h[3], 4)
	approx(t, "lowest", l[4], 1)
	approx(t, "lowest", l[2], 1)
}

func TestMACDConstantIsZero(t *testing.T) {
	src := make([]float64, 60)
	for i := range src {
		src[i] = 100
	}
	l, s, h := MACD(src, 12, 26, 9)
	approx(t, "line", l[59], 0)
	approx(t, "signal", s[59], 0)
	approx(t, "hist", h[59], 0)
	approx(t, "signal warmup", s[30], NaN)
}

func TestATRConstantRange(t *testing.T) {
	high := []float64{11, 11, 11, 11}
	low := []float64{9, 9, 9, 9}
	close := []float64{10, 10, 10, 10}
	approx(t, "atr", ATR(high, low, close, 2)[3], 2)
}

func TestShift(t *testing.T) {
	got := Shift([]float64{1, 2, 3}, 1)
	approx(t, "shift0", got[0], NaN)
	approx(t, "shift2", got[2], 2)
}
