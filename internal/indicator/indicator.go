// Package indicator implements technical indicators on float64 series.
//
// The formulas follow TradingView's Pine Script (ta.*) definitions so that
// values match the chart as closely as possible. Values that are not yet
// defined (warm-up period) are NaN, which plays the role of Pine's `na`.
package indicator

import "math"

// NaN is the "not available" value.
var NaN = math.NaN()

// IsNaN is a short alias for math.IsNaN.
func IsNaN(x float64) bool { return math.IsNaN(x) }

func nanSeries(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = NaN
	}
	return out
}

// SMA is the simple moving average (ta.sma).
func SMA(src []float64, length int) []float64 {
	out := nanSeries(len(src))
	if length <= 0 {
		return out
	}
	for i := length - 1; i < len(src); i++ {
		sum := 0.0
		ok := true
		for j := i - length + 1; j <= i; j++ {
			if IsNaN(src[j]) {
				ok = false
				break
			}
			sum += src[j]
		}
		if ok {
			out[i] = sum / float64(length)
		}
	}
	return out
}

// recursiveMA is the shared core of EMA and RMA: it is seeded with the SMA of
// the first `length` valid values and then applies out = a*src + (1-a)*prev.
func recursiveMA(src []float64, length int, alpha float64) []float64 {
	out := nanSeries(len(src))
	if length <= 0 {
		return out
	}
	start := 0
	for start < len(src) && IsNaN(src[start]) {
		start++
	}
	seed := start + length - 1
	if seed >= len(src) {
		return out
	}
	sum := 0.0
	for j := start; j <= seed; j++ {
		if IsNaN(src[j]) {
			return out // gaps inside the warm-up window: give up
		}
		sum += src[j]
	}
	prev := sum / float64(length)
	out[seed] = prev
	for i := seed + 1; i < len(src); i++ {
		if IsNaN(src[i]) {
			continue // keep prev, leave na at this bar
		}
		prev = alpha*src[i] + (1-alpha)*prev
		out[i] = prev
	}
	return out
}

// EMA is the exponential moving average (ta.ema), alpha = 2/(length+1).
func EMA(src []float64, length int) []float64 {
	return recursiveMA(src, length, 2/float64(length+1))
}

// RMA is Wilder's moving average (ta.rma), alpha = 1/length. Used by RSI and ATR.
func RMA(src []float64, length int) []float64 {
	return recursiveMA(src, length, 1/float64(length))
}

// WMA is the linearly weighted moving average (ta.wma).
func WMA(src []float64, length int) []float64 {
	out := nanSeries(len(src))
	if length <= 0 {
		return out
	}
	norm := float64(length*(length+1)) / 2
	for i := length - 1; i < len(src); i++ {
		sum := 0.0
		ok := true
		for k := 0; k < length; k++ {
			v := src[i-k]
			if IsNaN(v) {
				ok = false
				break
			}
			sum += v * float64(length-k)
		}
		if ok {
			out[i] = sum / norm
		}
	}
	return out
}

// RSI is the relative strength index (ta.rsi).
func RSI(src []float64, length int) []float64 {
	n := len(src)
	up := nanSeries(n)
	down := nanSeries(n)
	for i := 1; i < n; i++ {
		if IsNaN(src[i]) || IsNaN(src[i-1]) {
			continue
		}
		ch := src[i] - src[i-1]
		up[i] = math.Max(ch, 0)
		down[i] = math.Max(-ch, 0)
	}
	ru := RMA(up, length)
	rd := RMA(down, length)
	out := nanSeries(n)
	for i := range out {
		if IsNaN(ru[i]) || IsNaN(rd[i]) {
			continue
		}
		switch {
		case rd[i] == 0:
			out[i] = 100
		case ru[i] == 0:
			out[i] = 0
		default:
			out[i] = 100 - 100/(1+ru[i]/rd[i])
		}
	}
	return out
}

// MACD returns the MACD line, signal line and histogram (ta.macd).
func MACD(src []float64, fast, slow, signal int) (line, sig, hist []float64) {
	f := EMA(src, fast)
	s := EMA(src, slow)
	line = nanSeries(len(src))
	for i := range line {
		if !IsNaN(f[i]) && !IsNaN(s[i]) {
			line[i] = f[i] - s[i]
		}
	}
	sig = EMA(line, signal)
	hist = nanSeries(len(src))
	for i := range hist {
		if !IsNaN(line[i]) && !IsNaN(sig[i]) {
			hist[i] = line[i] - sig[i]
		}
	}
	return line, sig, hist
}

// Stdev is the population standard deviation over length bars (ta.stdev).
func Stdev(src []float64, length int) []float64 {
	mean := SMA(src, length)
	out := nanSeries(len(src))
	for i := range src {
		if IsNaN(mean[i]) {
			continue
		}
		ss := 0.0
		for j := i - length + 1; j <= i; j++ {
			d := src[j] - mean[i]
			ss += d * d
		}
		out[i] = math.Sqrt(ss / float64(length))
	}
	return out
}

// Bollinger returns the middle, upper and lower bands (ta.bb).
func Bollinger(src []float64, length int, mult float64) (mid, upper, lower []float64) {
	mid = SMA(src, length)
	sd := Stdev(src, length)
	upper = nanSeries(len(src))
	lower = nanSeries(len(src))
	for i := range src {
		if IsNaN(mid[i]) {
			continue
		}
		upper[i] = mid[i] + mult*sd[i]
		lower[i] = mid[i] - mult*sd[i]
	}
	return mid, upper, lower
}

// TrueRange is ta.tr(true): high-low on the first bar.
func TrueRange(high, low, close []float64) []float64 {
	out := nanSeries(len(close))
	for i := range close {
		hl := high[i] - low[i]
		if i == 0 {
			out[i] = hl
			continue
		}
		out[i] = math.Max(hl, math.Max(math.Abs(high[i]-close[i-1]), math.Abs(low[i]-close[i-1])))
	}
	return out
}

// ATR is the average true range (ta.atr).
func ATR(high, low, close []float64, length int) []float64 {
	return RMA(TrueRange(high, low, close), length)
}

// Highest returns the highest value over the last length bars (ta.highest).
func Highest(src []float64, length int) []float64 {
	return window(src, length, math.Max)
}

// Lowest returns the lowest value over the last length bars (ta.lowest).
func Lowest(src []float64, length int) []float64 {
	return window(src, length, math.Min)
}

func window(src []float64, length int, pick func(a, b float64) float64) []float64 {
	out := nanSeries(len(src))
	if length <= 0 {
		return out
	}
	for i := length - 1; i < len(src); i++ {
		v := src[i]
		ok := !IsNaN(v)
		for j := i - length + 1; j < i && ok; j++ {
			if IsNaN(src[j]) {
				ok = false
				break
			}
			v = pick(v, src[j])
		}
		if ok {
			out[i] = v
		}
	}
	return out
}

// Stoch is the raw stochastic (ta.stoch).
func Stoch(close, high, low []float64, length int) []float64 {
	hh := Highest(high, length)
	ll := Lowest(low, length)
	out := nanSeries(len(close))
	for i := range close {
		if IsNaN(hh[i]) || IsNaN(ll[i]) || hh[i] == ll[i] {
			continue
		}
		out[i] = 100 * (close[i] - ll[i]) / (hh[i] - ll[i])
	}
	return out
}

// StochKD returns %K = sma(stoch, smoothK) and %D = sma(%K, smoothD),
// matching TradingView's built-in Stochastic indicator.
func StochKD(close, high, low []float64, length, smoothK, smoothD int) (k, d []float64) {
	k = SMA(Stoch(close, high, low, length), smoothK)
	d = SMA(k, smoothD)
	return k, d
}

// ADX returns the average directional index together with +DI and -DI,
// matching TradingView's built-in DMI / ADX (diLength and adxSmoothing).
func ADX(high, low, close []float64, diLength, adxSmoothing int) (adx, plusDI, minusDI []float64) {
	n := len(close)
	plusDM := nanSeries(n)
	minusDM := nanSeries(n)
	for i := 1; i < n; i++ {
		up := high[i] - high[i-1]
		down := low[i-1] - low[i]
		plusDM[i], minusDM[i] = 0, 0
		if up > down && up > 0 {
			plusDM[i] = up
		}
		if down > up && down > 0 {
			minusDM[i] = down
		}
	}
	// The built-in DMI uses ta.tr without na handling: no true range on bar 0.
	rawTR := TrueRange(high, low, close)
	if n > 0 {
		rawTR[0] = NaN
	}
	tr := RMA(rawTR, diLength)
	sp := RMA(plusDM, diLength)
	sm := RMA(minusDM, diLength)
	plusDI = nanSeries(n)
	minusDI = nanSeries(n)
	dx := nanSeries(n)
	for i := 0; i < n; i++ {
		if IsNaN(tr[i]) || IsNaN(sp[i]) || IsNaN(sm[i]) || tr[i] == 0 {
			continue
		}
		plusDI[i] = 100 * sp[i] / tr[i]
		minusDI[i] = 100 * sm[i] / tr[i]
		sum := plusDI[i] + minusDI[i]
		if sum == 0 {
			dx[i] = 0
		} else {
			dx[i] = 100 * math.Abs(plusDI[i]-minusDI[i]) / sum
		}
	}
	adx = RMA(dx, adxSmoothing)
	return adx, plusDI, minusDI
}

// Shift returns src delayed by n bars (Pine's src[n]).
func Shift(src []float64, n int) []float64 {
	out := nanSeries(len(src))
	for i := n; i < len(src); i++ {
		if i-n >= 0 {
			out[i] = src[i-n]
		}
	}
	return out
}
