package signals

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	ind "github.com/matinnak/crypto-signal-bot/internal/indicator"
	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// --- order mechanics -------------------------------------------------------

func newTrade(long bool, legs ...leg) *trade {
	t := &trade{long: long, fill: 100, risk: 10, q: 2, rem: 2, legs: legs}
	if long {
		t.stop = 90
	} else {
		t.stop = 110
	}
	return t
}

func TestExitsTwoLegs(t *testing.T) {
	cash := 0.0
	tr := newTrade(true, leg{price: 110, qty: 1}, leg{price: 120, qty: 1})
	runExits(tr, barPath(100, 125, 99, 121), 0, &cash) // low first, then high
	if tr.rem != 0 || tr.reason != "target" || !near(tr.pnl, 10+20) {
		t.Fatalf("got %+v", tr)
	}
}

func TestExitsHighFirstThenStop(t *testing.T) {
	cash := 0.0
	tr := newTrade(true, leg{price: 110, qty: 1}, leg{price: 120, qty: 1})
	runExits(tr, barPath(108, 112, 85, 95), 0, &cash) // 108→112 (TP1) →85 (stop)
	if tr.rem != 0 || tr.reason != "stop" || !near(tr.pnl, 10-10) {
		t.Fatalf("got %+v", tr)
	}
}

func TestExitsSingleTargetAndGap(t *testing.T) {
	cash := 0.0
	tr := newTrade(false, leg{price: 75, qty: 2})
	runExits(tr, barPath(115, 116, 100, 101), 0, &cash) // short gapped above the stop
	if tr.rem != 0 || tr.reason != "stop" || !near(tr.pnl, -2*15) {
		t.Fatalf("gap: %+v", tr)
	}
	tr = newTrade(true, leg{price: 125, qty: 2})
	runExits(tr, barPath(100, 130, 95, 128), 0, &cash)
	if tr.rem != 0 || tr.reason != "target" || !near(tr.pnl, 2*25) {
		t.Fatalf("single target: %+v", tr)
	}
}

func TestLimitFill(t *testing.T) {
	// Buy limit at 100: open 105 closer to low -> 105→98 crosses 100.
	px, rest, ok := limitFill(true, 100, barPath(105, 110, 98, 108))
	if !ok || px != 100 || rest[0] != 100 || rest[1] != 98 {
		t.Fatalf("fill %v %v %v", px, rest, ok)
	}
	// Gap below the level: fills at the open.
	if px, _, _ := limitFill(true, 100, barPath(97, 103, 96, 99)); px != 97 {
		t.Fatalf("gap fill %v", px)
	}
	// Never reaches the level.
	if _, _, ok := limitFill(false, 120, barPath(105, 110, 98, 108)); ok {
		t.Fatal("should not fill")
	}
}

// --- simulation with a scripted setup -------------------------------------

func flatBars(n int, price float64) []market.Candle {
	out := make([]market.Candle, n)
	for i := range out {
		o := t0.Add(time.Duration(i) * 2 * time.Hour)
		out[i] = market.Candle{OpenTime: o, CloseTime: o.Add(2*time.Hour - time.Millisecond), Open: price, High: price + 1, Low: price - 1, Close: price}
	}
	return out
}

func limitSpec() *Spec {
	return &Spec{ID: "t", Bar: 2 * time.Hour, Targets: []Target{{2, 100}}, LimitValid: 3, Risk: DefaultRisk}
}

func TestLimitOrderCancelledAfterValidBars(t *testing.T) {
	bars := flatBars(10, 100)
	st := newSetup(len(bars))
	st.Long[2], st.RefLong[2], st.StopLong[2] = true, 90, 80 // limit far below: never fills
	st.Long[4] = true                                        // blocked while pending
	st.RefLong[4], st.StopLong[4] = 90, 80
	res := Simulate(limitSpec(), bars, nil, st)
	kinds := []string{}
	for _, e := range res.Events {
		kinds = append(kinds, string(e.Kind)+"@"+e.Bar.OpenTime.Format("15"))
	}
	// signal at bar 2 (04h), cancelled at the close of bar 5 (10h).
	if strings.Join(kinds, ",") != "signal@04,cancelled@10" {
		t.Fatalf("events: %v", kinds)
	}
}

func TestLimitOrderFillsAndHitsTarget(t *testing.T) {
	bars := flatBars(10, 100)
	bars[3].Low = 94 // fills buy limit at 95
	bars[5].High = 120
	st := newSetup(len(bars))
	st.Long[2], st.RefLong[2], st.StopLong[2] = true, 95, 90 // R=5, target 105
	res := Simulate(limitSpec(), bars, nil, st)
	var got []string
	for _, e := range res.Events {
		got = append(got, string(e.Kind))
	}
	if strings.Join(got, ",") != "signal,filled,exit" {
		t.Fatalf("events %v", got)
	}
	ex := res.Events[2]
	if ex.ExitReason != "target" || !near(ex.ResultR, 2) || res.Events[1].Fill != 95 {
		t.Fatalf("exit %+v", ex)
	}
}

func TestMarketEntryNextOpenAndOnePositionAtATime(t *testing.T) {
	bars := flatBars(12, 100)
	bars[3].Open = 101 // entry fill
	bars[7].Low = 80   // stop out
	st := newSetup(len(bars))
	for _, i := range []int{2, 4, 9} {
		st.Long[i], st.RefLong[i], st.StopLong[i] = true, 100, 95
	}
	spec := &Spec{ID: "m", Bar: 2 * time.Hour, Targets: []Target{{3, 100}}, Risk: DefaultRisk}
	res := Simulate(spec, bars, nil, st)
	var got []string
	for _, e := range res.Events {
		got = append(got, string(e.Kind)+"@"+e.Bar.OpenTime.Format("15"))
	}
	// bar 4's signal is ignored (in position); stop at bar 7 (14h); new signal at bar 9 (18h).
	if strings.Join(got, ",") != "signal@04,exit@14,signal@18" {
		t.Fatalf("events %v", got)
	}
	// bar 9's signal fills at bar 10's open: an open position remains.
	if res.Events[1].Fill != 101 || res.Position == nil || res.Position.Pending || !near(res.Events[1].ResultR, -6.0/5) {
		t.Fatalf("fill/position: %+v %+v", res.Events[1], res.Position)
	}
}

// --- strategy rules on synthetic markets ---------------------------------

func synthetic(n int, step time.Duration, trend float64) []market.Candle {
	out := make([]market.Candle, n)
	prev := 100.0
	for i := range out {
		x := float64(i)
		c := 100 + trend*x + 6*math.Sin(x/5) + 2.5*math.Sin(x/1.7) + 1.2*math.Sin(x*2.9)
		if i%97 >= 90 { // occasional sharp moves (breakouts)
			c += 2.2 * float64(i%97-89) * math.Copysign(1, math.Sin(x/40))
		}
		o := t0.Add(time.Duration(i) * step)
		out[i] = market.Candle{OpenTime: o, CloseTime: o.Add(step - time.Millisecond), Open: prev,
			High: math.Max(prev, c) + 0.8, Low: math.Min(prev, c) - 0.8, Close: c, Volume: 1}
		prev = c
	}
	return out
}

func TestAllStrategiesInvariants(t *testing.T) {
	for _, s := range All {
		for _, trend := range []float64{0.05, -0.05, 0} {
			step, _ := market.IntervalDuration(s.Interval)
			raw := synthetic(3000, step, trend)
			bars := raw
			if s.Bar > step {
				bars = market.Aggregate(raw, s.Bar)
			}
			h4 := market.Aggregate(raw, 4*time.Hour)
			d1 := market.Aggregate(raw, 24*time.Hour)
			setup := s.Build(bars, h4)
			res := Simulate(s, bars, d1, setup)

			idx := map[time.Time]int{}
			for i, b := range bars {
				idx[b.OpenTime] = i
			}
			busy := false // order or position open
			counts := map[EventKind]int{}
			for _, e := range res.Events {
				counts[e.Kind]++
				i := idx[e.Bar.OpenTime]
				switch e.Kind {
				case Signal:
					if busy {
						t.Fatalf("%s: signal while busy at bar %d", s.ID, i)
					}
					busy = true
					if e.Long != setup.Long[i] && !e.Long != setup.Short[i] {
						t.Fatalf("%s: signal without setup at %d", s.ID, i)
					}
					r := math.Abs(e.Entry - e.Stop)
					if (e.Long && e.Stop >= e.Entry) || (!e.Long && e.Stop <= e.Entry) {
						t.Fatalf("%s: stop on wrong side %+v", s.ID, e)
					}
					last := e.Targets[len(e.Targets)-1]
					wantR := s.Targets[len(s.Targets)-1].R
					if !near(math.Abs(last-e.Entry), wantR*r) {
						t.Fatalf("%s: target not at %gR", s.ID, wantR)
					}
				case Exit, Cancelled:
					if !busy {
						t.Fatalf("%s: %s without order", s.ID, e.Kind)
					}
					busy = false
				}
			}
			if busy != (res.Position != nil) {
				t.Fatalf("%s: final state mismatch", s.ID)
			}
			if trend != 0 && counts[Signal] == 0 {
				t.Errorf("%s trend %v: no signals", s.ID, trend)
			}
			if s.LimitValid == 0 && counts[Filled]+counts[Cancelled] > 0 {
				t.Errorf("%s: limit events on a market strategy", s.ID)
			}
			if !s.UseBE && counts[TP1] > 0 {
				t.Errorf("%s: TP1 events without break-even", s.ID)
			}
			t.Logf("%-15s trend %+.2f: %4d bars %v", s.ID, trend, len(bars), counts)
		}
	}
}

func TestBNBSwingBreak(t *testing.T) {
	// A clear swing high at bar 10 (price 110), confirmed at bar 12; bar 15 closes above it.
	n := 40
	bars := flatBars(n, 100)
	for i := range bars {
		bars[i].High, bars[i].Low = 101, 99
	}
	bars[10].High = 110
	bars[15].Close, bars[15].High = 112, 113
	st := BNBBOSRetest.Build(bars, nil)
	for i := 0; i < n; i++ {
		if st.Long[i] != (i == 15) {
			t.Fatalf("bar %d long=%v", i, st.Long[i])
		}
	}
	if st.RefLong[15] != 110 || !(st.StopLong[15] < 110) {
		t.Fatalf("level %v stop %v", st.RefLong[15], st.StopLong[15])
	}
	if !strings.Contains(st.Note(15, true), "BOS") {
		t.Errorf("note %q", st.Note(15, true))
	}
}

// --- runner ------------------------------------------------------------------

type fakeMarket struct{}

func (fakeMarket) Candles(_ context.Context, _, interval string, limit int) ([]market.Candle, error) {
	d, _ := market.IntervalDuration(interval)
	c := synthetic(limit, d, 0.03)
	// shift so the data ends now-ish
	shift := time.Now().Truncate(d).Add(-time.Duration(limit) * d).Sub(t0)
	for i := range c {
		c[i].OpenTime = c[i].OpenTime.Add(shift)
		c[i].CloseTime = c[i].CloseTime.Add(shift)
	}
	return c, nil
}

type failMarket struct{}

func (failMarket) Candles(context.Context, string, string, int) ([]market.Candle, error) {
	return nil, context.DeadlineExceeded
}

type sink struct{ msgs []string }

func (s *sink) SendMessage(_ context.Context, _ int64, text string) error {
	s.msgs = append(s.msgs, text)
	return nil
}

func TestRunnerFallbackNotifyOnceAndStatus(t *testing.T) {
	snk := &sink{}
	r := &Runner{Specs: All, Futures: failMarket{}, Spot: fakeMarket{}, Notify: snk, ChatID: 1}
	snaps := r.Evaluate(context.Background())
	if len(snaps) != len(All) {
		t.Fatalf("snapshots %d", len(snaps))
	}
	for _, s := range snaps {
		if s.Err != nil || !strings.Contains(s.Source, "Spot") || (s.Spec.SpotOnly && strings.Contains(s.Source, "فیوچرز")) {
			t.Fatalf("%s: err %v source %q", s.Spec.ID, s.Err, s.Source)
		}
	}
	// Pretend each strategy's last event just happened.
	for _, s := range snaps {
		if n := len(s.Result.Events); n > 0 {
			s.At = s.Result.Events[n-1].Bar.OpenTime.Add(s.Spec.Bar + time.Minute)
		}
	}
	st := store.New()
	n, err := r.NotifyNew(context.Background(), snaps, st)
	if err != nil || n == 0 {
		t.Fatalf("sent %d err %v", n, err)
	}
	if again, _ := r.NotifyNew(context.Background(), snaps, st); again != 0 {
		t.Fatalf("re-sent %d", again)
	}
	status := r.StatusText(snaps)
	for _, sym := range []string{"BTCUSDT | 3h", "BNBUSDT | 2h", "SOLUSDT | 2h", "XRPUSDT | 2h", "ETHUSDT | 1h"} {
		if !strings.Contains(status, sym) {
			t.Errorf("status missing %s", sym)
		}
	}
}

func TestFormatSignalMessages(t *testing.T) {
	r := &Runner{}
	bar := market.Candle{OpenTime: t0}
	e := Event{Kind: Signal, Bar: bar, Long: true, Entry: 0.5123, Stop: 0.5, Targets: []float64{0.54305}}
	msg := r.FormatEvent(XRPMACDMomentum, e, "src")
	for _, want := range []string{"XRPUSDT | 2h", "ورود: ~0.5123", "حد ضرر: 0.5000", "تارگت: 0.5431", "2.5R"} {
		if !strings.Contains(msg, want) {
			t.Errorf("XRP message missing %q:\n%s", want, msg)
		}
	}
	e = Event{Kind: Signal, Bar: bar, Long: false, Entry: 600, Stop: 612, Targets: []float64{570}}
	msg = r.FormatEvent(BNBBOSRetest, e, "src")
	if !strings.Contains(msg, "لیمیت") || !strings.Contains(msg, "رد شد") {
		t.Errorf("BNB message:\n%s", msg)
	}
	e = Event{Kind: Signal, Bar: bar, Long: true, Entry: 95000, Stop: 94000, Targets: []float64{96000, 97000}}
	msg = r.FormatEvent(BTCMTFMACD, e, "src")
	if !strings.Contains(msg, "تارگت ۱: 96,000.0") || !strings.Contains(msg, "تارگت ۲: 97,000.0") {
		t.Errorf("BTC message:\n%s", msg)
	}
}

func TestPrice(t *testing.T) {
	cases := []struct {
		v    float64
		d    int
		want string
	}{{95210.46, 1, "95,210.5"}, {0.51234, 4, "0.5123"}, {612.5, 2, "612.50"}, {-1500, 1, "-1,500.0"}}
	for _, c := range cases {
		if got := Price(c.v, c.d); got != c.want {
			t.Errorf("Price(%v,%d)=%q want %q", c.v, c.d, got, c.want)
		}
	}
}

func TestTimeExitClosesAtNextOpen(t *testing.T) {
	bars := flatBars(20, 100)
	bars[3].Open = 100 // fill
	bars[9].Open = 103 // time-exit fill: 5 bars after the fill (bar 3) -> ordered at close of 8
	st := newSetup(len(bars))
	st.Long[2], st.RefLong[2], st.StopLong[2] = true, 100, 90 // R=10, target 120 never reached
	st.Long[9], st.RefLong[9], st.StopLong[9] = true, 100, 90 // flat again at bar 9's close
	spec := &Spec{ID: "time", Bar: 2 * time.Hour, Targets: []Target{{2, 100}}, MaxBars: 5, Risk: DefaultRisk}
	res := Simulate(spec, bars, nil, st)
	var got []string
	for _, e := range res.Events {
		got = append(got, string(e.Kind)+"@"+e.Bar.OpenTime.Format("15"))
	}
	// trade 1: fill bar 3, time exit ordered at close of bar 8, filled at bar 9's open (18h);
	// trade 2: signal bar 9, fill bar 10, time exit filled at bar 16's open (08h next day).
	if strings.Join(got, ",") != "signal@04,exit@18,signal@18,exit@08" {
		t.Fatalf("events %v", got)
	}
	ex := res.Events[1]
	if ex.ExitReason != "time" || !near(ex.ResultR, 0.3) {
		t.Fatalf("exit %+v", ex)
	}
	msg := (&Runner{}).FormatEvent(ETHBBBreakout, ex, "")
	if !strings.Contains(msg, "خروج زمانی بعد از 72 کندل") {
		t.Errorf("exit message: %s", msg)
	}
}

func TestETHZScore(t *testing.T) {
	// Flat prices then a jump: Z is far above 2.75 on the jump bar only.
	bars := flatBars(60, 100)
	for i := range bars {
		bars[i].Close = 100 + 0.1*math.Sin(float64(i))
		bars[i].Open = bars[i].Close
	}
	bars[50].Close, bars[50].High = 103, 103.5
	st := ETHBBBreakout.Build(bars, nil)
	if !st.Long[50] || st.Short[50] || st.Long[49] {
		t.Fatalf("long50=%v short50=%v long49=%v", st.Long[50], st.Short[50], st.Long[49])
	}
	if !near(st.StopLong[50], 103-3*indATR(bars)[50]) {
		t.Errorf("stop %v", st.StopLong[50])
	}
	if !strings.Contains(st.Note(50, true), "Z-score") {
		t.Errorf("note %q", st.Note(50, true))
	}
}

func indATR(bars []market.Candle) []float64 {
	_, h, l, c := series(bars)
	return ind.ATR(h, l, c, 14)
}

func TestLateNote(t *testing.T) {
	if lateNote(10*time.Minute) != "" {
		t.Error("short delay should have no note")
	}
	if n := lateNote(4*time.Hour + 40*time.Minute); !strings.Contains(n, "4 ساعت و 40 دقیقه") {
		t.Errorf("note %q", n)
	}
	if n := lateNote(5 * time.Hour); !strings.Contains(n, "5 ساعت تأخیر") {
		t.Errorf("note %q", n)
	}
}
