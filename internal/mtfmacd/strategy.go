// Package mtfmacd is a Go port of the Pine Script strategy
// "BTC MTF MACD Trend-Pullback v1.0":
//
//   - chart: 3h candles
//   - trend: previous closed 4h candle, close above / below EMA(50)
//   - entry: MACD(12,26,9) crosses its signal line at a 3h close, market order next open
//   - stop:  lowest low / highest high of the last 5 bars -/+ 0.25 x ATR(14)
//   - exits: TP1 = 1R (50%), TP2 = 2R (50%), stop to break-even after TP1
//   - one position at a time, daily / weekly loss guards
//
// Because new entries are only allowed while flat, the strategy's positions are
// simulated bar by bar with the same rules as TradingView's broker emulator, so
// signals match the chart.
package mtfmacd

import (
	"math"
	"time"

	ind "github.com/matinnak/crypto-signal-bot/internal/indicator"
	"github.com/matinnak/crypto-signal-bot/internal/market"
)

// Params mirrors the Pine inputs (defaults = the script's defaults).
type Params struct {
	HTF            time.Duration // trend timeframe (4h)
	HTFEmaLen      int
	StrengthEmaLen int // daily EMA, informational
	MACDFast       int
	MACDSlow       int
	MACDSignal     int
	SwingLen       int
	SwingBuf       float64 // x ATR
	ATRLen         int
	TP1R, TP1Pct   float64
	TP2R, TP2Pct   float64
	UseBE          bool
	RiskPct        float64
	MaxLev         float64
	DayLossPct     float64
	WeekLossPct    float64
	CommissionPct  float64
	InitialCapital float64
}

// Default returns the script's default inputs.
func Default() Params {
	return Params{
		HTF: 4 * time.Hour, HTFEmaLen: 50, StrengthEmaLen: 20,
		MACDFast: 12, MACDSlow: 26, MACDSignal: 9,
		SwingLen: 5, SwingBuf: 0.25, ATRLen: 14,
		TP1R: 1, TP1Pct: 50, TP2R: 2, TP2Pct: 50, UseBE: true,
		RiskPct: 0.5, MaxLev: 5, DayLossPct: 2, WeekLossPct: 4,
		CommissionPct: 0.07, InitialCapital: 10000,
	}
}

// EventKind is what happened at a bar close.
type EventKind string

// Event kinds.
const (
	Signal EventKind = "signal" // new entry signal (order for next bar open)
	TP1    EventKind = "tp1"    // TP1 filled, stop moved to break-even
	Exit   EventKind = "exit"   // position fully closed
)

// Event is something worth notifying, reported at the close of Bar.
type Event struct {
	Kind EventKind
	Bar  market.Candle // 3h bar at whose close the event is reported
	Long bool

	// Signal / trade levels
	Entry    float64 // signal-bar close (reference price)
	Stop     float64
	TP1, TP2 float64
	Strong   bool    // daily close vs EMA agrees (informational)
	HTFClose float64 // previous closed 4h close
	HTFEma   float64

	// TP1 / exit
	NewStop    float64 // TP1: stop after break-even move
	FillEntry  float64 // actual entry fill (next bar open)
	ExitReason string  // "tp2", "stop", "breakeven"
	ResultR    float64 // P&L in R, before fees
	EntryBar   time.Time
}

// Position is the open trade at the end of the data, if any.
type Position struct {
	Long        bool
	SignalBar   time.Time
	Entry       float64 // fill price
	Stop        float64
	TP1, TP2    float64
	TP1Done     bool
	RiskDist    float64
	PendingFill bool // signal on the last bar, entry fills at the next open
}

// Result is the outcome of a simulation.
type Result struct {
	Bars     []market.Candle // 3h bars used
	Events   []Event
	Position *Position
	Equity   float64
}

// htfValues returns, for each chart bar, the previous closed HTF bar's close
// and EMA (Pine: request.security(tf, expr[1], lookahead_on)), i.e. the HTF
// bar before the one that contains the chart bar's open time.
func htfValues(bars, htf []market.Candle, d time.Duration, emaLen int) (cl, ema []float64) {
	closes := make([]float64, len(htf))
	idx := make(map[int64]int, len(htf))
	for i, c := range htf {
		closes[i] = c.Close
		idx[c.OpenTime.UnixMilli()] = i
	}
	e := ind.EMA(closes, emaLen)
	cl = make([]float64, len(bars))
	ema = make([]float64, len(bars))
	ms := d.Milliseconds()
	for i, b := range bars {
		cl[i], ema[i] = math.NaN(), math.NaN()
		containing := (b.OpenTime.UnixMilli() / ms) * ms
		if j, ok := idx[containing-ms]; ok {
			cl[i], ema[i] = closes[j], e[j]
		}
	}
	return cl, ema
}

// dailyValues is htfValues for 1d bars (aligned to UTC midnight).
func dailyValues(bars, daily []market.Candle, emaLen int) (cl, ema []float64) {
	return htfValues(bars, daily, 24*time.Hour, emaLen)
}

func floor6(x float64) float64 { return math.Floor(x*1e6) / 1e6 }

type openTrade struct {
	long       bool
	signalBar  time.Time
	fill       float64
	stop       float64
	tp1, tp2   float64
	risk       float64
	q, q1, q2  float64 // total, TP1 leg, TP2 leg (rest)
	rem        float64
	tp1Done    bool
	beDone     bool
	pnl        float64 // realised price P&L (qty * price diff), before fees
	exitReason string
}

// Run simulates the strategy over closed 3h bars, with 4h and 1d candles for
// the trend and strength filters.
func Run(p Params, bars, h4, d1 []market.Candle) Result {
	n := len(bars)
	o, h, l, c := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i, b := range bars {
		o[i], h[i], l[i], c[i] = b.Open, b.High, b.Low, b.Close
	}
	macd, sig, _ := ind.MACD(c, p.MACDFast, p.MACDSlow, p.MACDSignal)
	atr := ind.ATR(h, l, c, p.ATRLen)
	lowest := ind.Lowest(l, p.SwingLen)
	highest := ind.Highest(h, p.SwingLen)
	htfClose, htfEma := htfValues(bars, h4, p.HTF, p.HTFEmaLen)
	strClose, strEma := dailyValues(bars, d1, p.StrengthEmaLen)

	res := Result{Bars: bars}
	cash := p.InitialCapital
	fee := p.CommissionPct / 100
	dayEq, weekEq := p.InitialCapital, p.InitialCapital

	var pos *openTrade
	var pending *openTrade // entry ordered at previous close

	closeLeg := func(t *openTrade, qty, price float64) {
		dir := 1.0
		if !t.long {
			dir = -1
		}
		t.pnl += dir * qty * (price - t.fill)
		cash += dir*qty*(price-t.fill) - qty*price*fee
		t.rem -= qty
		if t.rem < 1e-9 {
			t.rem = 0
		}
	}

	for i := 0; i < n; i++ {
		// --- intrabar: fill pending entry at the open, then run exits.
		if pending != nil {
			pos = pending
			pending = nil
			pos.fill = o[i]
			cash -= pos.q * pos.fill * fee
		}
		if pos != nil {
			simulateBar(pos, o[i], h[i], l[i], c[i], closeLeg)
		}

		// --- bar close
		equity := cash
		if pos != nil && pos.rem > 0 {
			dir := 1.0
			if !pos.long {
				dir = -1
			}
			equity += dir * pos.rem * (c[i] - pos.fill)
		}
		t := bars[i].OpenTime
		if i > 0 && t.YearDay() != bars[i-1].OpenTime.YearDay() {
			dayEq = equity
		}
		if i > 0 && isoWeek(t) != isoWeek(bars[i-1].OpenTime) {
			weekEq = equity
		}
		dayBlocked := p.DayLossPct > 0 && equity <= dayEq*(1-p.DayLossPct/100)
		weekBlocked := p.WeekLossPct > 0 && equity <= weekEq*(1-p.WeekLossPct/100)
		canTrade := !dayBlocked && !weekBlocked

		if pos != nil {
			if pos.rem == 0 {
				r := pos.pnl / (pos.q * pos.risk)
				res.Events = append(res.Events, Event{Kind: Exit, Bar: bars[i], Long: pos.long, ExitReason: pos.exitReason,
					ResultR: r, FillEntry: pos.fill, Stop: pos.stop, TP1: pos.tp1, TP2: pos.tp2, EntryBar: pos.signalBar})
				pos = nil
			} else if p.UseBE && pos.tp1Done && !pos.beDone {
				// Management at close: stop -> break-even (entry fill price).
				pos.beDone = true
				if pos.long {
					pos.stop = math.Max(pos.stop, pos.fill)
				} else {
					pos.stop = math.Min(pos.stop, pos.fill)
				}
				res.Events = append(res.Events, Event{Kind: TP1, Bar: bars[i], Long: pos.long, NewStop: pos.stop,
					FillEntry: pos.fill, TP1: pos.tp1, TP2: pos.tp2, EntryBar: pos.signalBar})
			}
		}
		// --- new entries (flat only)
		if pos != nil || i == 0 || math.IsNaN(atr[i]) {
			continue
		}
		bullX := macd[i] > sig[i] && macd[i-1] <= sig[i-1]
		bearX := macd[i] < sig[i] && macd[i-1] >= sig[i-1]
		trendUp := htfClose[i] > htfEma[i]
		trendDn := htfClose[i] < htfEma[i]
		longSig := bullX && trendUp
		shortSig := bearX && trendDn

		slLong := lowest[i] - p.SwingBuf*atr[i]
		slShort := highest[i] + p.SwingBuf*atr[i]
		distL := c[i] - slLong
		distS := slShort - c[i]
		riskAmt := equity * p.RiskPct / 100
		qty := func(dist float64) float64 {
			if !(dist > 0) {
				return 0
			}
			return floor6(math.Min(riskAmt/dist, equity*p.MaxLev/c[i]))
		}
		qL, qS := qty(distL), qty(distS)
		goLong := canTrade && longSig && qL > 0
		goShort := canTrade && shortSig && !goLong && qS > 0
		if !goLong && !goShort {
			continue
		}

		long := goLong
		sl, q, dir := slShort, qS, -1.0
		if long {
			sl, q, dir = slLong, qL, 1.0
		}
		risk := math.Abs(c[i] - sl)
		tr := &openTrade{
			long: long, signalBar: bars[i].OpenTime, stop: sl, risk: risk,
			tp1: c[i] + dir*p.TP1R*risk, tp2: c[i] + dir*p.TP2R*risk,
			q: q, q1: floor6(q * p.TP1Pct / 100), rem: q,
		}
		tr.q2 = q - tr.q1
		pending = tr
		strong := strClose[i] > strEma[i]
		if !long {
			strong = strClose[i] < strEma[i]
		}
		res.Events = append(res.Events, Event{Kind: Signal, Bar: bars[i], Long: long, Entry: c[i], Stop: sl,
			TP1: tr.tp1, TP2: tr.tp2, Strong: strong, HTFClose: htfClose[i], HTFEma: htfEma[i]})
	}

	switch {
	case pending != nil:
		res.Position = &Position{Long: pending.long, SignalBar: pending.signalBar, Entry: bars[n-1].Close,
			Stop: pending.stop, TP1: pending.tp1, TP2: pending.tp2, RiskDist: pending.risk, PendingFill: true}
	case pos != nil:
		res.Position = &Position{Long: pos.long, SignalBar: pos.signalBar, Entry: pos.fill, Stop: pos.stop,
			TP1: pos.tp1, TP2: pos.tp2, TP1Done: pos.tp1Done, RiskDist: pos.risk}
	}
	res.Equity = cash
	return res
}

func isoWeek(t time.Time) int {
	y, w := t.ISOWeek()
	return y*100 + w
}

// simulateBar walks the bar's assumed intrabar path (TradingView rule: if the
// open is closer to the high, open→high→low→close, else open→low→high→close)
// and fills the stop and take-profit orders.
func simulateBar(t *openTrade, o, h, l, c float64, closeLeg func(*openTrade, float64, float64)) {
	path := []float64{o, l, h, c}
	if h-o <= o-l {
		path = []float64{o, h, l, c}
	}
	stopHit := func(price float64) bool {
		if t.long {
			return price <= t.stop
		}
		return price >= t.stop
	}
	reached := func(price, target float64) bool {
		if t.long {
			return price >= target
		}
		return price <= target
	}
	exitAll := func(price float64) {
		closeLeg(t, t.rem, price)
		switch {
		case t.tp1Done && t.beDone:
			t.exitReason = "breakeven"
		default:
			t.exitReason = "stop"
		}
	}
	fillTPs := func(price float64, atOpen bool) {
		if !t.tp1Done && reached(price, t.tp1) {
			px := t.tp1
			if atOpen {
				px = price
			}
			t.tp1Done = true
			closeLeg(t, t.q1, px)
		}
		if t.tp1Done && t.rem > 0 && reached(price, t.tp2) {
			px := t.tp2
			if atOpen {
				px = price
			}
			closeLeg(t, t.rem, px)
			t.exitReason = "tp2"
		}
	}

	// Gap at the open.
	if stopHit(o) {
		exitAll(o)
		return
	}
	fillTPs(o, true)

	for k := 1; k < len(path) && t.rem > 0; k++ {
		from, to := path[k-1], path[k]
		adverse := (t.long && to < from) || (!t.long && to > from)
		if adverse {
			if stopHit(to) {
				exitAll(t.stop)
			}
		} else {
			fillTPs(to, false)
		}
	}
}
