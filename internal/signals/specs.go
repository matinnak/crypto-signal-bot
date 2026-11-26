package signals

import (
	"fmt"
	"math"
	"time"

	ind "github.com/matinnak/crypto-signal-bot/internal/indicator"
	"github.com/matinnak/crypto-signal-bot/internal/market"
)

// Spec defines one built-in strategy.
type Spec struct {
	ID       string
	Name     string
	Symbol   string        // Binance symbol, e.g. BTCUSDT
	Interval string        // Binance interval to download
	Bar      time.Duration // chart bar; when larger than Interval, candles are aggregated
	NeedH4   bool          // also download 4h candles (trend filter)
	Decimals int           // price decimals in messages

	Targets    []Target
	UseBE      bool // move stop to break-even after the first target
	LimitValid int  // > 0: limit entry valid for this many bars; 0: market entry
	Risk       Risk

	Rules   string // short Persian description
	Warning string // research status from the script header

	Build func(bars, h4 []market.Candle) Setup
}

func series(bars []market.Candle) (o, h, l, c []float64) {
	n := len(bars)
	o, h, l, c = make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i, b := range bars {
		o[i], h[i], l[i], c[i] = b.Open, b.High, b.Low, b.Close
	}
	return
}

func newSetup(n int) Setup {
	nan := func() []float64 {
		s := make([]float64, n)
		for i := range s {
			s[i] = math.NaN()
		}
		return s
	}
	return Setup{Long: make([]bool, n), Short: make([]bool, n),
		RefLong: nan(), RefShort: nan(), StopLong: nan(), StopShort: nan()}
}

func crossUp(a, b []float64, i int) bool   { return i > 0 && a[i] > b[i] && a[i-1] <= b[i-1] }
func crossDown(a, b []float64, i int) bool { return i > 0 && a[i] < b[i] && a[i-1] >= b[i-1] }

// atrStops sets market-entry levels at the close with stop = close ∓ mult × ATR.
func atrStops(st *Setup, c, atr []float64, mult float64, i int) {
	st.RefLong[i], st.RefShort[i] = c[i], c[i]
	st.StopLong[i] = c[i] - mult*atr[i]
	st.StopShort[i] = c[i] + mult*atr[i]
}

// BTCMTFMACD — "BTC MTF MACD Trend-Pullback v1.0", BTCUSDT 3h.
var BTCMTFMACD = &Spec{
	ID: "btc_mtf_macd", Name: "BTC MTF MACD Trend-Pullback v1.0",
	Symbol: "BTCUSDT", Interval: "1h", Bar: 3 * time.Hour, NeedH4: true, Decimals: 1,
	Targets: []Target{{1, 50}, {2, 50}}, UseBE: true,
	Risk: func() Risk { r := DefaultRisk; r.FloorQty = true; return r }(),
	Rules: "روند: کندل بسته‌شده‌ی قبلی 4h نسبت به EMA50 | ورود: کراس MACD(12,26,9) | " +
		"حد ضرر: کف/سقف ۵ کندل ∓ 0.25×ATR | تارگت: 1R (50%) و 2R (50%)، بعد از تارگت ۱ حد ضرر سربه‌سر",
	Warning: "در آزمون خارج از نمونه تأیید نشده — فقط معامله‌ی آزمایشی",
	Build: func(bars, h4 []market.Candle) Setup {
		_, h, l, c := series(bars)
		st := newSetup(len(bars))
		macd, sig, _ := ind.MACD(c, 12, 26, 9)
		atr := ind.ATR(h, l, c, 14)
		lo, hi := ind.Lowest(l, 5), ind.Highest(h, 5)
		htfC, htfE := htfPrev(bars, h4, 4*time.Hour, 50)
		for i := range bars {
			st.RefLong[i], st.RefShort[i] = c[i], c[i]
			st.StopLong[i] = lo[i] - 0.25*atr[i]
			st.StopShort[i] = hi[i] + 0.25*atr[i]
			st.Long[i] = crossUp(macd, sig, i) && htfC[i] > htfE[i]
			st.Short[i] = crossDown(macd, sig, i) && htfC[i] < htfE[i]
		}
		st.Note = func(i int, long bool) string {
			if long {
				return "روند 4h: صعودی (بالای EMA50)"
			}
			return "روند 4h: نزولی (زیر EMA50)"
		}
		return st
	},
}

// BNBBOSRetest — "BNB BOS Retest 2h v1.0", BNBUSDT 2h, limit entry at the broken level.
var BNBBOSRetest = &Spec{
	ID: "bnb_bos_retest", Name: "BNB BOS Retest 2h v1.0",
	Symbol: "BNBUSDT", Interval: "2h", Bar: 2 * time.Hour, Decimals: 2,
	Targets: []Target{{2.5, 100}}, LimitValid: 6, Risk: DefaultRisk,
	Rules: "سقف/کف (۵ چپ، ۲ راست) | بسته شدن کندل 2h بالای آخرین سقف (زیر آخرین کف) | " +
		"ورود: لیمیت روی سطح شکسته‌شده، اعتبار ۶ کندل | حد ضرر پشت سقف/کف مخالف (محدود به ATR) | تارگت: 2.5R",
	Warning: "در آزمون خارج از نمونه رد شد — فقط برای پژوهش و معامله‌ی آزمایشی، نه پول واقعی",
	Build: func(bars, _ []market.Candle) Setup {
		_, h, l, c := series(bars)
		n := len(bars)
		st := newSetup(n)
		atr := ind.ATR(h, l, c, 14)
		const pivL, pivR = 5, 2
		const minGap, stopBuf, maxStop = 0.5, 0.3, 3.0
		hh, ll := ind.Highest(h, pivL+pivR+1), ind.Lowest(l, pivL+pivR+1)

		shV, slV := math.NaN(), math.NaN()
		shI, slI, usedH, usedL, trend := -1, -1, -1, -1, 0
		kinds := make([]string, n)
		for i := 0; i < n; i++ {
			if i >= pivR && h[i-pivR] == hh[i] {
				shV, shI = h[i-pivR], i-pivR
			}
			if i >= pivR && l[i-pivR] == ll[i] {
				slV, slI = l[i-pivR], i-pivR
			}
			prev := trend
			switch {
			case !math.IsNaN(shV) && shI != usedH && c[i] > shV:
				usedH, trend = shI, 1
				lv, a := shV, atr[i]
				base := slV
				if math.IsNaN(base) {
					base = lv - maxStop*a
				}
				st.Long[i] = true
				st.RefLong[i] = lv
				st.StopLong[i] = math.Max(math.Min(base, lv-minGap*a)-stopBuf*a, lv-maxStop*a)
				kinds[i] = map[bool]string{true: "CHoCH", false: "BOS"}[prev == -1]
			case !math.IsNaN(slV) && slI != usedL && c[i] < slV:
				usedL, trend = slI, -1
				lv, a := slV, atr[i]
				base := shV
				if math.IsNaN(base) {
					base = lv + maxStop*a
				}
				st.Short[i] = true
				st.RefShort[i] = lv
				st.StopShort[i] = math.Min(math.Max(base, lv+minGap*a)+stopBuf*a, lv+maxStop*a)
				kinds[i] = map[bool]string{true: "CHoCH", false: "BOS"}[prev == 1]
			}
		}
		st.Note = func(i int, long bool) string {
			if long {
				return kinds[i] + " — بسته شدن 2h بالای آخرین سقف"
			}
			return kinds[i] + " — بسته شدن 2h زیر آخرین کف"
		}
		return st
	},
}

// SOLStochReversion — "SOL Stoch Reversion 2h v1.0", SOLUSDT 2h.
var SOLStochReversion = &Spec{
	ID: "sol_stoch_rev", Name: "SOL Stoch Reversion 2h v1.0",
	Symbol: "SOLUSDT", Interval: "2h", Bar: 2 * time.Hour, Decimals: 2,
	Targets: []Target{{1.5, 100}}, Risk: DefaultRisk,
	Rules: "کراس %K/%D استوکستیک (14,3,3) از زیر ۲۰ (خرید) یا بالای ۸۰ (فروش) وقتی ADX(14) < 20 | " +
		"ورود: مارکت | حد ضرر: ∓ 1.5×ATR | تارگت: 1.5R",
	Warning: "در آزمون خارج از نمونه تأیید نشده — فقط معامله‌ی آزمایشی",
	Build: func(bars, _ []market.Candle) Setup {
		_, h, l, c := series(bars)
		st := newSetup(len(bars))
		k, d := ind.StochKD(c, h, l, 14, 3, 3)
		adx, _, _ := ind.ADX(h, l, c, 14, 14)
		atr := ind.ATR(h, l, c, 14)
		for i := range bars {
			atrStops(&st, c, atr, 1.5, i)
			if i == 0 || !(adx[i] < 20) {
				continue
			}
			st.Long[i] = crossUp(k, d, i) && k[i-1] < 20
			st.Short[i] = crossDown(k, d, i) && k[i-1] > 80
		}
		st.Note = func(i int, long bool) string {
			return fmt.Sprintf("ADX %.1f < 20 (بدون روند قوی) | Stoch %%K %.1f", adx[i], k[i])
		}
		return st
	},
}

// XRPMACDMomentum — "XRP MACD Momentum 2h v1.0", XRPUSDT 2h.
var XRPMACDMomentum = &Spec{
	ID: "xrp_macd_mom", Name: "XRP MACD Momentum 2h v1.0",
	Symbol: "XRPUSDT", Interval: "2h", Bar: 2 * time.Hour, Decimals: 4,
	Targets: []Target{{2.5, 100}}, Risk: DefaultRisk,
	Rules:   "کراس MACD(12,26,9) | ورود: مارکت | حد ضرر: ∓ 2×ATR | تارگت: 2.5R",
	Warning: "در آزمون خارج از نمونه تأیید نشده — فقط معامله‌ی آزمایشی",
	Build: func(bars, _ []market.Candle) Setup {
		_, h, l, c := series(bars)
		st := newSetup(len(bars))
		macd, sig, _ := ind.MACD(c, 12, 26, 9)
		atr := ind.ATR(h, l, c, 14)
		for i := range bars {
			atrStops(&st, c, atr, 2, i)
			st.Long[i] = crossUp(macd, sig, i)
			st.Short[i] = crossDown(macd, sig, i)
		}
		return st
	},
}

// All is the list of strategies the bot runs.
var All = []*Spec{BTCMTFMACD, BNBBOSRetest, SOLStochReversion, XRPMACDMomentum}
