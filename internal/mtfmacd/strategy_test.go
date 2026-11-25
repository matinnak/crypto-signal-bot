package mtfmacd

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

func newTrade(long bool) *openTrade {
	t := &openTrade{long: long, fill: 100, risk: 10, q: 2, q1: 1, q2: 1, rem: 2}
	if long {
		t.stop, t.tp1, t.tp2 = 90, 110, 120
	} else {
		t.stop, t.tp1, t.tp2 = 110, 90, 80
	}
	return t
}

func legCloser(fills *[]float64) func(*openTrade, float64, float64) {
	return func(t *openTrade, q, px float64) {
		*fills = append(*fills, px)
		t.rem -= q
	}
}

func TestSimulateBarTP1ThenTP2(t *testing.T) {
	var fills []float64
	tr := newTrade(true)
	simulateBar(tr, 100, 125, 99, 121, legCloser(&fills)) // open far from high -> low first, then high
	if !tr.tp1Done || tr.rem != 0 || tr.exitReason != "tp2" || len(fills) != 2 || fills[0] != 110 || fills[1] != 120 {
		t.Fatalf("got %+v fills %v", tr, fills)
	}
}

func TestSimulateBarHighFirstThenStop(t *testing.T) {
	var fills []float64
	tr := newTrade(true)
	// open 108 closer to high 112 -> path 108 -> 112 (TP1) -> 85 (stop on rest) -> 95
	simulateBar(tr, 108, 112, 85, 95, legCloser(&fills))
	if !tr.tp1Done || tr.rem != 0 || tr.exitReason != "stop" || fills[0] != 110 || fills[1] != 90 {
		t.Fatalf("got %+v fills %v", tr, fills)
	}
}

func TestSimulateBarLowFirstStopsBeforeTarget(t *testing.T) {
	var fills []float64
	tr := newTrade(true)
	// open 95 closer to low 89 -> path 95 -> 89 (stop) ; target never reached
	simulateBar(tr, 95, 130, 89, 120, legCloser(&fills))
	if tr.tp1Done || tr.rem != 0 || fills[0] != 90 {
		t.Fatalf("got %+v fills %v", tr, fills)
	}
}

func TestSimulateBarGapThroughStop(t *testing.T) {
	var fills []float64
	tr := newTrade(false) // short, stop 110
	simulateBar(tr, 115, 116, 100, 101, legCloser(&fills))
	if tr.rem != 0 || fills[0] != 115 || tr.exitReason != "stop" {
		t.Fatalf("gap stop should fill at open: %+v %v", tr, fills)
	}
}

func TestSimulateBarBreakEven(t *testing.T) {
	var fills []float64
	tr := newTrade(true)
	tr.tp1Done, tr.beDone, tr.stop, tr.rem = true, true, 100, 1
	simulateBar(tr, 105, 108, 99, 100, legCloser(&fills))
	if tr.rem != 0 || tr.exitReason != "breakeven" || fills[0] != 100 {
		t.Fatalf("got %+v %v", tr, fills)
	}
}

func candles(start time.Time, d time.Duration, closes []float64) []market.Candle {
	out := make([]market.Candle, len(closes))
	prev := closes[0]
	for i, c := range closes {
		o := start.Add(time.Duration(i) * d)
		hi, lo := math.Max(prev, c), math.Min(prev, c)
		out[i] = market.Candle{OpenTime: o, CloseTime: o.Add(d - time.Millisecond),
			Open: prev, High: hi + 50, Low: lo - 50, Close: c}
		prev = c
	}
	return out
}

func TestHTFValuesUsesPreviousClosedBar(t *testing.T) {
	h4 := candles(t0, 4*time.Hour, []float64{10, 20, 30, 40})
	bars := candles(t0, 3*time.Hour, []float64{1, 1, 1, 1, 1})
	cl, _ := htfValues(bars, h4, 4*time.Hour, 1)
	// 3h bar opens: 00,03,06,09,12 -> containing 4h: 00,00,04,08,12 -> previous: none,none,00,04,08
	want := []float64{math.NaN(), math.NaN(), 10, 20, 30}
	for i := range want {
		if math.IsNaN(want[i]) != math.IsNaN(cl[i]) || (!math.IsNaN(want[i]) && cl[i] != want[i]) {
			t.Errorf("bar %d: got %v want %v", i, cl[i], want[i])
		}
	}
}

// synthetic market: trend plus waves, so MACD crosses often.
func synthetic(n int, d time.Duration, trend float64) []market.Candle {
	closes := make([]float64, n)
	for i := range closes {
		x := float64(i)
		closes[i] = 50000 + trend*x + 1500*math.Sin(x/6) + 400*math.Sin(x/2.3)
	}
	return candles(t0, d, closes)
}

func TestRunInvariants(t *testing.T) {
	for _, trend := range []float64{25, -25, 0} {
		h1 := synthetic(3000, time.Hour, trend/3)
		bars := market.Aggregate(h1, 3*time.Hour)
		h4 := market.Aggregate(h1, 4*time.Hour)
		d1 := market.Aggregate(h1, 24*time.Hour)
		p := Default()
		res := Run(p, bars, h4, d1)

		closes := make([]float64, len(bars))
		for i, b := range bars {
			closes[i] = b.Close
		}
		macd, sig, _ := ind.MACD(closes, 12, 26, 9)
		htfC, htfE := htfValues(bars, h4, 4*time.Hour, 50)
		index := map[time.Time]int{}
		for i, b := range bars {
			index[b.OpenTime] = i
		}

		signals, open := 0, false
		for _, e := range res.Events {
			switch e.Kind {
			case Signal:
				if open {
					t.Fatalf("trend %v: signal at %v while a position was open", trend, e.Bar.OpenTime)
				}
				open = true
				signals++
				i := index[e.Bar.OpenTime]
				if e.Long {
					if !(macd[i] > sig[i] && macd[i-1] <= sig[i-1]) || !(htfC[i] > htfE[i]) {
						t.Fatalf("long signal without its conditions at bar %d", i)
					}
					if !(e.Stop < e.Entry && near(e.TP1-e.Entry, e.Entry-e.Stop) && near(e.TP2-e.Entry, 2*(e.Entry-e.Stop))) {
						t.Fatalf("bad long levels %+v", e)
					}
				} else {
					if !(macd[i] < sig[i] && macd[i-1] >= sig[i-1]) || !(htfC[i] < htfE[i]) {
						t.Fatalf("short signal without its conditions at bar %d", i)
					}
					if !(e.Stop > e.Entry && near(e.Entry-e.TP1, e.Stop-e.Entry)) {
						t.Fatalf("bad short levels %+v", e)
					}
				}
			case Exit:
				if !open {
					t.Fatalf("exit without position")
				}
				open = false
			case TP1:
				if !open {
					t.Fatalf("tp1 without position")
				}
			}
		}
		if trend != 0 && signals == 0 {
			t.Errorf("trend %v: expected some signals", trend)
		}
		if open != (res.Position != nil) {
			t.Errorf("trend %v: final position mismatch", trend)
		}
		t.Logf("trend %v: %d bars, %d signals, final equity %.0f", trend, len(bars), signals, res.Equity)
	}
}

type fakeMarket struct{ h1 []market.Candle }

func (f fakeMarket) Candles(_ context.Context, _, interval string, limit int) ([]market.Candle, error) {
	var d time.Duration
	switch interval {
	case "1h":
		d = time.Hour
	case "4h":
		d = 4 * time.Hour
	default:
		d = 24 * time.Hour
	}
	c := market.Aggregate(f.h1, d)
	if d == time.Hour {
		c = f.h1
	}
	if len(c) > limit {
		c = c[len(c)-limit:]
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

func TestRunnerNotifiesOnceAndFallsBack(t *testing.T) {
	h1 := synthetic(3000, time.Hour, 8)
	snk := &sink{}
	r := &Runner{Futures: failMarket{}, Spot: fakeMarket{h1}, Notify: snk, ChatID: 1, Params: Default()}

	snapAll, err := r.Evaluate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapAll.Source, "Spot") {
		t.Errorf("expected spot fallback, got %q", snapAll.Source)
	}
	// Pick the last signal and pretend "now" is just after its bar closed.
	var last Event
	for _, e := range snapAll.Events {
		if e.Kind == Signal {
			last = e
		}
	}
	if last.Kind == "" {
		t.Fatal("no signal in synthetic data")
	}
	snap := *snapAll
	snap.At = last.Bar.OpenTime.Add(Bar + 5*time.Minute)

	st := store.New()
	n, err := r.NotifyNew(context.Background(), &snap, st)
	if err != nil || n < 1 {
		t.Fatalf("sent %d, err %v", n, err)
	}
	if !strings.Contains(strings.Join(snk.msgs, "\n"), "حد ضرر: "+Price(last.Stop)) {
		t.Errorf("signal message missing stop:\n%s", strings.Join(snk.msgs, "\n---\n"))
	}
	n, _ = r.NotifyNew(context.Background(), &snap, st)
	if n != 0 {
		t.Errorf("events re-sent: %d", n)
	}
	if !strings.Contains(r.StatusText(snapAll), "سیگنال‌های اخیر") {
		t.Errorf("status text missing recent signals")
	}
}

func TestPrice(t *testing.T) {
	cases := map[float64]string{95210.46: "95,210.5", 999.9: "999.9", 1234567: "1,234,567.0", -1500: "-1,500.0"}
	for in, want := range cases {
		if got := Price(in); got != want {
			t.Errorf("Price(%v)=%q want %q", in, got, want)
		}
	}
}
