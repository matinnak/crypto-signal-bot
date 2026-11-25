package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
)

type fakeMarket struct {
	candles []market.Candle
	calls   int
}

func (f *fakeMarket) Candles(_ context.Context, _, _ string, _ int) ([]market.Candle, error) {
	f.calls++
	return f.candles, nil
}

type fakeNotifier struct{ msgs []string }

func (f *fakeNotifier) SendMessage(_ context.Context, _ int64, text string) error {
	f.msgs = append(f.msgs, text)
	return nil
}

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func hourly(closes ...float64) []market.Candle {
	var out []market.Candle
	for i, c := range closes {
		open := t0.Add(time.Duration(i) * time.Hour)
		out = append(out, market.Candle{
			OpenTime: open, CloseTime: open.Add(time.Hour - time.Millisecond),
			Open: c, High: c, Low: c, Close: c, Volume: 1,
		})
	}
	return out
}

func setup(closes ...float64) (*Engine, *fakeMarket, *fakeNotifier, *store.State) {
	m := &fakeMarket{candles: hourly(closes...)}
	n := &fakeNotifier{}
	st := store.New()
	st.Strategies["up"] = &store.Strategy{Name: "up", Buy: "close > close[1]", Sell: "close < close[1]"}
	st.Users[1] = &store.User{ChatID: 1, Approved: true, Subs: []*store.Subscription{{Symbol: "BTCUSDT", Interval: "1h", Strategy: "up"}}}
	st.Users[2] = &store.User{ChatID: 2, Approved: false, Subs: []*store.Subscription{{Symbol: "BTCUSDT", Interval: "1h", Strategy: "up"}}}
	e := &Engine{Market: m, Notify: n, Loc: time.UTC}
	return e, m, n, st
}

func TestCheckFirstRunOnlyLatestBarAndSkipsOpenCandle(t *testing.T) {
	// 4 candles; the 4th (index 3) is still open at "now".
	e, _, n, st := setup(1, 2, 3, 2)
	e.Now = func() time.Time { return t0.Add(3*time.Hour + 30*time.Minute) }

	sent, err := e.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	// Latest closed bar is index 2 (3 > 2): one BUY, only for the approved user.
	if sent != 1 || !strings.Contains(n.msgs[0], "BUY") {
		t.Fatalf("expected 1 buy, got %d %v", sent, n.msgs)
	}
	if got := st.Users[1].Subs[0].LastChecked; got != t0.Add(2*time.Hour).UnixMilli() {
		t.Errorf("LastChecked = %d", got)
	}

	// Running again without a new candle must not resend.
	sent, _ = e.Check(context.Background(), st)
	if sent != 0 {
		t.Fatalf("duplicate signal sent")
	}
}

func TestCheckCatchesUpMissedBars(t *testing.T) {
	e, _, n, st := setup(1, 2, 3, 2, 1, 5)
	st.Users[1].Subs[0].LastChecked = t0.Add(2 * time.Hour).UnixMilli() // bars 0..2 seen
	e.Now = func() time.Time { return t0.Add(6*time.Hour + 10*time.Minute) }

	sent, err := e.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	// bars 3 (sell), 4 (sell), 5 (buy)
	if sent != 3 {
		t.Fatalf("expected 3 signals, got %d: %v", sent, n.msgs)
	}
	if !strings.Contains(n.msgs[2], "BUY") || !strings.Contains(n.msgs[0], "SELL") {
		t.Errorf("unexpected order: %v", n.msgs)
	}
}

func TestCheckFetchesEachPairOnce(t *testing.T) {
	e, m, _, st := setup(1, 2, 3)
	st.Strategies["down"] = &store.Strategy{Name: "down", Sell: "close < 0"}
	st.Users[1].Subs = append(st.Users[1].Subs, &store.Subscription{Symbol: "BTCUSDT", Interval: "1h", Strategy: "down"})
	e.Now = func() time.Time { return t0.Add(10 * time.Hour) }
	if _, err := e.Check(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 {
		t.Errorf("expected 1 fetch, got %d", m.calls)
	}
}

func TestBacktest(t *testing.T) {
	m := &fakeMarket{candles: hourly(1, 2, 3, 2, 1, 5)}
	s := &store.Strategy{Name: "up", Buy: "close > close[1]"}
	sigs, bars, err := Backtest(context.Background(), m, s, "BTCUSDT", "1h", t0.Add(10*time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if bars != 6 || len(sigs) != 2 || sigs[1].Candle.Close != 5 {
		t.Fatalf("bars=%d sigs=%+v", bars, sigs)
	}
}

func TestCompileStrategyNeedsACondition(t *testing.T) {
	if _, err := CompileStrategy(&store.Strategy{Name: "x"}); err == nil {
		t.Error("expected error for empty strategy")
	}
}

func TestNeedsFetch(t *testing.T) {
	now := t0.Add(5*time.Hour + 10*time.Minute) // last closed 1h candle opened at 04:00
	lastClosed := t0.Add(4 * time.Hour).UnixMilli()
	if needsFetch("1h", now, lastClosed) {
		t.Error("nothing new: should not fetch")
	}
	if !needsFetch("1h", now, lastClosed-time.Hour.Milliseconds()) {
		t.Error("one new candle: should fetch")
	}
	if !needsFetch("1h", now, 0) {
		t.Error("never checked: should fetch")
	}
	if !needsFetch("1w", now, lastClosed) {
		t.Error("weekly: always fetch")
	}
}

func TestCheckSkipsStaleBars(t *testing.T) {
	e, _, n, st := setup(1, 2, 3, 2, 1, 5)
	st.Users[1].Subs[0].LastChecked = t0.UnixMilli()
	e.Now = func() time.Time { return t0.Add(8 * time.Hour) } // bars closing before 05:00 are stale
	sent, err := e.Check(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	// Only bars 4 (closes 05:00, sell) and 5 (closes 06:00, buy) are recent enough.
	if sent != 2 {
		t.Fatalf("expected 2 signals, got %d: %v", sent, n.msgs)
	}
}
