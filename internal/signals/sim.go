// Package signals runs built-in, hard-coded trading strategies (Go ports of
// Pine Script strategies) and reports their signals.
//
// All strategies share one simulation that follows TradingView's broker
// emulator: orders are placed at a bar close, market entries fill at the next
// open, limit entries fill when price trades through the level, and exits
// (stop + take-profit legs) are checked along the assumed intrabar path. Only
// one order or position exists at a time, so simulating positions is needed for
// signals to match the chart.
package signals

import (
	"math"
	"time"

	ind "github.com/matinnak/crypto-signal-bot/internal/indicator"
	"github.com/matinnak/crypto-signal-bot/internal/market"
)

// Target is one take-profit leg: price at R multiples, closing Pct% of the position.
type Target struct {
	R   float64
	Pct float64
}

// Risk holds money-management inputs shared by all strategies.
type Risk struct {
	RiskPct        float64
	MaxLev         float64
	DayLossPct     float64
	WeekLossPct    float64
	CommissionPct  float64
	InitialCapital float64
	FloorQty       bool // round quantities down to 1e-6 (as the BTC script does)
}

// DefaultRisk is the money management used by every supplied script.
var DefaultRisk = Risk{RiskPct: 0.5, MaxLev: 5, DayLossPct: 2, WeekLossPct: 4, CommissionPct: 0.07, InitialCapital: 10000}

// Setup is a strategy's per-bar signal output, computed before simulation.
type Setup struct {
	Long, Short         []bool
	RefLong, RefShort   []float64 // entry reference: signal close (market) or limit level
	StopLong, StopShort []float64
	// Note optionally describes the market condition at a signal bar.
	Note func(i int, long bool) string
}

// EventKind is what happened at a bar close.
type EventKind string

// Event kinds.
const (
	Signal    EventKind = "signal"    // new entry order (market next open, or limit)
	Filled    EventKind = "filled"    // limit order filled
	Cancelled EventKind = "cancelled" // limit order expired unfilled
	TP1       EventKind = "tp1"       // first target filled and stop moved to break-even
	Exit      EventKind = "exit"      // position fully closed
)

// Event is reported at the close of Bar.
type Event struct {
	Kind    EventKind
	Bar     market.Candle
	Long    bool
	Entry   float64   // reference entry (signal close or limit level)
	Stop    float64   // initial stop (signal) / current stop
	Targets []float64 // target prices
	Strong  bool      // daily close vs EMA20 agrees (informational)
	Note    string

	Fill       float64 // actual entry fill price
	NewStop    float64 // TP1: stop after the break-even move
	ExitReason string  // "target", "stop", "breakeven"
	ResultR    float64 // P&L in R, before fees
	SignalBar  time.Time
}

// Position is the open order or trade at the end of the data.
type Position struct {
	Long      bool
	SignalBar time.Time
	Entry     float64 // fill price, or reference price when not filled yet
	Stop      float64
	Targets   []float64
	Legs      int // legs already filled
	Pending   bool
	Limit     bool
	BarsLeft  int // pending limit: bars until cancellation
}

// Result is the outcome of a simulation.
type Result struct {
	Bars     []market.Candle
	Events   []Event
	Position *Position
}

type leg struct {
	price, qty float64
	done       bool
}

type trade struct {
	long      bool
	signalBar time.Time
	signalIdx int
	ref       float64 // reference entry price (for R)
	fill      float64
	stop      float64
	risk      float64
	q, rem    float64
	legs      []leg
	beDone    bool
	pnl       float64
	reason    string
	filled    bool
	strong    bool
	note      string
}

func (t *trade) dir() float64 {
	if t.long {
		return 1
	}
	return -1
}

func (t *trade) targets() []float64 {
	out := make([]float64, len(t.legs))
	for i, l := range t.legs {
		out[i] = l.price
	}
	return out
}

func (t *trade) legsDone() int {
	n := 0
	for _, l := range t.legs {
		if l.done {
			n++
		}
	}
	return n
}

// barPath is TradingView's intrabar assumption: if the open is closer to the
// high, open→high→low→close, otherwise open→low→high→close.
func barPath(o, h, l, c float64) []float64 {
	if h-o <= o-l {
		return []float64{o, h, l, c}
	}
	return []float64{o, l, h, c}
}

// limitFill finds where a limit order at lv fills along path. It returns the
// fill price and the remaining path starting at the fill.
func limitFill(long bool, lv float64, path []float64) (float64, []float64, bool) {
	hit := func(p float64) bool {
		if long {
			return p <= lv
		}
		return p >= lv
	}
	if hit(path[0]) {
		return path[0], path, true // gap through the level: fills at the open
	}
	for k := 1; k < len(path); k++ {
		if hit(path[k]) {
			rest := append([]float64{lv}, path[k:]...)
			return lv, rest, true
		}
	}
	return 0, nil, false
}

// runExits walks the path and fills the stop and take-profit legs.
func runExits(t *trade, path []float64, fee float64, cash *float64) {
	closeQty := func(qty, price float64) {
		t.pnl += t.dir() * qty * (price - t.fill)
		*cash += t.dir()*qty*(price-t.fill) - qty*price*fee
		t.rem -= qty
		if t.rem < 1e-12 {
			t.rem = 0
		}
	}
	stopHit := func(p float64) bool {
		if t.long {
			return p <= t.stop
		}
		return p >= t.stop
	}
	reached := func(p, target float64) bool {
		if t.long {
			return p >= target
		}
		return p <= target
	}
	exitAll := func(price float64) {
		closeQty(t.rem, price)
		if t.beDone {
			t.reason = "breakeven"
		} else {
			t.reason = "stop"
		}
	}
	fillLegs := func(p float64, atStart bool) {
		for k := range t.legs {
			lg := &t.legs[k]
			if lg.done || t.rem == 0 || !reached(p, lg.price) {
				continue
			}
			px := lg.price
			if atStart {
				px = p
			}
			qty := lg.qty
			if k == len(t.legs)-1 || qty > t.rem {
				qty = t.rem // last leg closes whatever is left
			}
			lg.done = true
			closeQty(qty, px)
			if t.rem == 0 {
				t.reason = "target"
			}
		}
	}

	if stopHit(path[0]) {
		exitAll(path[0])
		return
	}
	fillLegs(path[0], true)
	for k := 1; k < len(path) && t.rem > 0; k++ {
		from, to := path[k-1], path[k]
		adverse := (t.long && to < from) || (!t.long && to > from)
		if adverse {
			if stopHit(to) {
				exitAll(t.stop)
			}
		} else {
			fillLegs(to, false)
		}
	}
}

func isoWeek(t time.Time) int {
	y, w := t.ISOWeek()
	return y*100 + w
}

// htfPrev returns, for each chart bar, the close and EMA of the HTF bar before
// the one containing the chart bar's open (Pine: request.security(tf, x[1],
// lookahead_on)).
func htfPrev(bars, htf []market.Candle, d time.Duration, emaLen int) (cl, ema []float64) {
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
		if j, ok := idx[(b.OpenTime.UnixMilli()/ms)*ms-ms]; ok {
			cl[i], ema[i] = closes[j], e[j]
		}
	}
	return cl, ema
}

func floor6(x float64) float64 { return math.Floor(x*1e6) / 1e6 }

// Simulate runs the strategy's setup through the broker emulation.
func Simulate(s *Spec, bars, d1 []market.Candle, st Setup) Result {
	n := len(bars)
	r := s.Risk
	fee := r.CommissionPct / 100
	strC, strE := htfPrev(bars, d1, 24*time.Hour, 20)

	res := Result{Bars: bars}
	cash := r.InitialCapital
	dayEq, weekEq := r.InitialCapital, r.InitialCapital
	var pos, pending *trade

	for i := 0; i < n; i++ {
		b := bars[i]
		path := barPath(b.Open, b.High, b.Low, b.Close)
		filledNow := false

		// --- intrabar: fill the pending order, then run exits.
		if pending != nil {
			if s.LimitValid == 0 {
				pending.fill, pending.filled = b.Open, true
				pos, pending, filledNow = pending, nil, true
				cash -= pos.q * pos.fill * fee
				runExits(pos, path, fee, &cash)
			} else if px, rest, ok := limitFill(pending.long, pending.ref, path); ok {
				pending.fill, pending.filled = px, true
				pos, pending, filledNow = pending, nil, true
				cash -= pos.q * pos.fill * fee
				runExits(pos, rest, fee, &cash)
			}
		} else if pos != nil {
			runExits(pos, path, fee, &cash)
		}

		// --- bar close: equity and loss guards.
		equity := cash
		if pos != nil && pos.rem > 0 {
			equity += pos.dir() * pos.rem * (b.Close - pos.fill)
		}
		if i > 0 && b.OpenTime.YearDay() != bars[i-1].OpenTime.YearDay() {
			dayEq = equity
		}
		if i > 0 && isoWeek(b.OpenTime) != isoWeek(bars[i-1].OpenTime) {
			weekEq = equity
		}
		canTrade := !(r.DayLossPct > 0 && equity <= dayEq*(1-r.DayLossPct/100)) &&
			!(r.WeekLossPct > 0 && equity <= weekEq*(1-r.WeekLossPct/100))

		// --- pending limit order: an unfilled order blocks new orders this bar,
		// and is cancelled after LimitValid bars.
		limBlock := pending != nil
		if pending != nil && i >= pending.signalIdx+s.LimitValid {
			res.Events = append(res.Events, Event{Kind: Cancelled, Bar: b, Long: pending.long, Entry: pending.ref,
				Stop: pending.stop, Targets: pending.targets(), SignalBar: pending.signalBar})
			pending = nil
		}

		// --- position management at the close.
		if pos != nil {
			if filledNow && s.LimitValid > 0 {
				res.Events = append(res.Events, Event{Kind: Filled, Bar: b, Long: pos.long, Entry: pos.ref, Fill: pos.fill,
					Stop: pos.stop, Targets: pos.targets(), SignalBar: pos.signalBar})
			}
			if pos.rem == 0 {
				res.Events = append(res.Events, Event{Kind: Exit, Bar: b, Long: pos.long, Entry: pos.ref, Fill: pos.fill,
					Stop: pos.stop, Targets: pos.targets(), ExitReason: pos.reason,
					ResultR: pos.pnl / (pos.q * pos.risk), SignalBar: pos.signalBar})
				pos = nil
			} else if s.UseBE && pos.legs[0].done && !pos.beDone {
				pos.beDone = true
				if pos.long {
					pos.stop = math.Max(pos.stop, pos.fill)
				} else {
					pos.stop = math.Min(pos.stop, pos.fill)
				}
				res.Events = append(res.Events, Event{Kind: TP1, Bar: b, Long: pos.long, Fill: pos.fill, NewStop: pos.stop,
					Targets: pos.targets(), SignalBar: pos.signalBar})
			}
		}

		// --- new entries: flat, nothing pending, guards ok.
		if pos != nil || pending != nil || limBlock || !canTrade {
			continue
		}
		long := st.Long[i]
		if !long && !st.Short[i] {
			continue
		}
		ref, stop := st.RefShort[i], st.StopShort[i]
		if long {
			ref, stop = st.RefLong[i], st.StopLong[i]
		}
		dist := math.Abs(ref - stop)
		if math.IsNaN(dist) || dist <= 0 || (long && stop >= ref) || (!long && stop <= ref) {
			continue
		}
		riskAmt := equity * r.RiskPct / 100
		q := math.Min(riskAmt/dist, equity*r.MaxLev/ref)
		if r.FloorQty {
			q = floor6(q)
		}
		if !(q > 0) {
			continue
		}
		t := &trade{long: long, signalBar: b.OpenTime, signalIdx: i, ref: ref, stop: stop, risk: dist, q: q, rem: q}
		dir := t.dir()
		for _, tg := range s.Targets {
			lq := q * tg.Pct / 100
			if r.FloorQty {
				lq = floor6(lq)
			}
			t.legs = append(t.legs, leg{price: ref + dir*tg.R*dist, qty: lq})
		}
		t.strong = strC[i] > strE[i]
		if !long {
			t.strong = strC[i] < strE[i]
		}
		if st.Note != nil {
			t.note = st.Note(i, long)
		}
		pending = t
		res.Events = append(res.Events, Event{Kind: Signal, Bar: b, Long: long, Entry: ref, Stop: stop,
			Targets: t.targets(), Strong: t.strong, Note: t.note, SignalBar: b.OpenTime})
	}

	last := n - 1
	switch {
	case pending != nil:
		left := 0
		if s.LimitValid > 0 {
			left = pending.signalIdx + s.LimitValid - last
		}
		res.Position = &Position{Long: pending.long, SignalBar: pending.signalBar, Entry: pending.ref, Stop: pending.stop,
			Targets: pending.targets(), Pending: true, Limit: s.LimitValid > 0, BarsLeft: left}
	case pos != nil:
		res.Position = &Position{Long: pos.long, SignalBar: pos.signalBar, Entry: pos.fill, Stop: pos.stop,
			Targets: pos.targets(), Legs: pos.legsDone()}
	}
	return res
}
