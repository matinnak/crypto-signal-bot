// Package engine checks every subscription for new signals and sends them.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/dsl"
	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
)

// CandleLimit is how many candles are fetched per symbol. More history makes
// recursive indicators such as EMA(200) and RSI converge to TradingView's values.
const CandleLimit = 1000

// MaxCatchUp is how many missed closed candles are evaluated at most when the
// previous run was late (e.g. a 15m timeframe checked only once an hour).
const MaxCatchUp = 8

// Notifier sends a text message to a chat.
type Notifier interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Engine evaluates strategies on market data.
type Engine struct {
	Market  market.Provider
	Notify  Notifier
	Loc     *time.Location
	Now     func() time.Time
	Workers int
	Log     *log.Logger
}

// Side is the direction of a signal.
type Side string

// Signal sides.
const (
	Buy  Side = "buy"
	Sell Side = "sell"
)

// Signal is a strategy condition that became true on a closed candle.
type Signal struct {
	Side     Side
	Symbol   string
	Interval string
	Strategy string
	Candle   market.Candle
}

// Compiled holds the parsed conditions of a strategy.
type Compiled struct {
	Buy, Sell *dsl.Condition
}

// CompileStrategy parses a strategy's buy and sell conditions. At least one is required.
func CompileStrategy(s *store.Strategy) (*Compiled, error) {
	if strings.TrimSpace(s.Buy) == "" && strings.TrimSpace(s.Sell) == "" {
		return nil, errors.New("حداقل یکی از شرط‌های buy یا sell لازم است")
	}
	c := &Compiled{}
	var err error
	if strings.TrimSpace(s.Buy) != "" {
		if c.Buy, err = dsl.Compile(s.Buy); err != nil {
			return nil, fmt.Errorf("شرط buy: %w", err)
		}
	}
	if strings.TrimSpace(s.Sell) != "" {
		if c.Sell, err = dsl.Compile(s.Sell); err != nil {
			return nil, fmt.Errorf("شرط sell: %w", err)
		}
	}
	return c, nil
}

// evaluated is the per-bar result of a compiled strategy.
type evaluated struct{ buy, sell []bool }

func (c *Compiled) eval(d *dsl.Data) evaluated {
	var e evaluated
	if c.Buy != nil {
		e.buy = c.Buy.Eval(d)
	}
	if c.Sell != nil {
		e.sell = c.Sell.Eval(d)
	}
	return e
}

func (e evaluated) at(i int) []Side {
	var out []Side
	if e.buy != nil && e.buy[i] {
		out = append(out, Buy)
	}
	if e.sell != nil && e.sell[i] {
		out = append(out, Sell)
	}
	return out
}

// ToData converts candles to the series used by the dsl package.
func ToData(c []market.Candle) *dsl.Data {
	d := &dsl.Data{
		Open:   make([]float64, len(c)),
		High:   make([]float64, len(c)),
		Low:    make([]float64, len(c)),
		Close:  make([]float64, len(c)),
		Volume: make([]float64, len(c)),
	}
	for i, k := range c {
		d.Open[i], d.High[i], d.Low[i], d.Close[i], d.Volume[i] = k.Open, k.High, k.Low, k.Close, k.Volume
	}
	return d
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log.Printf(format, args...)
	}
}

type pairKey struct{ symbol, interval string }

type fetchResult struct {
	candles []market.Candle
	err     error
}

// fetchAll downloads candles for every pair concurrently using a worker pool.
func (e *Engine) fetchAll(ctx context.Context, pairs []pairKey) map[pairKey]fetchResult {
	workers := e.Workers
	if workers <= 0 {
		workers = 4
	}
	jobs := make(chan pairKey)
	results := make(map[pairKey]fetchResult, len(pairs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				c, err := e.Market.Candles(ctx, p.symbol, p.interval, CandleLimit)
				mu.Lock()
				results[p] = fetchResult{c, err}
				mu.Unlock()
			}
		}()
	}
	for _, p := range pairs {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return results
}

// Check evaluates all subscriptions of approved users, sends new signals and
// updates each subscription's LastChecked. It returns the number of signals sent.
func (e *Engine) Check(ctx context.Context, st *store.State) (int, error) {
	compiled := map[string]*Compiled{}
	for name, s := range st.Strategies {
		c, err := CompileStrategy(s)
		if err != nil {
			e.logf("strategy %s does not compile: %v", name, err)
			continue
		}
		compiled[name] = c
	}

	type target struct {
		user *store.User
		sub  *store.Subscription
	}
	byPair := map[pairKey][]target{}
	for _, u := range st.Users {
		if !u.Approved {
			continue
		}
		for _, s := range u.Subs {
			k := pairKey{s.Symbol, s.Interval}
			byPair[k] = append(byPair[k], target{u, s})
		}
	}
	if len(byPair) == 0 {
		return 0, nil
	}
	pairs := make([]pairKey, 0, len(byPair))
	for k := range byPair {
		pairs = append(pairs, k)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].symbol != pairs[j].symbol {
			return pairs[i].symbol < pairs[j].symbol
		}
		return pairs[i].interval < pairs[j].interval
	})

	now := e.now()
	// Skip pairs whose newest closed candle has already been evaluated by every
	// subscriber; this keeps frequent checks (serve mode) cheap.
	due := pairs[:0]
	for _, p := range pairs {
		if needsFetch(p.interval, now, minLastChecked(byPair[p], func(t target) int64 { return t.sub.LastChecked })) {
			due = append(due, p)
		}
	}
	pairs = due

	fetched := e.fetchAll(ctx, pairs)
	sent := 0
	var errs []error

	for _, p := range pairs {
		res := fetched[p]
		if res.err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", p.symbol, p.interval, res.err))
			continue
		}
		candles := market.ClosedOnly(res.candles, now)
		if len(candles) == 0 {
			continue
		}
		data := ToData(candles)
		last := len(candles) - 1
		lastMs := candles[last].OpenTime.UnixMilli()
		cache := map[string]evaluated{}

		for _, t := range byPair[p] {
			c, ok := compiled[t.sub.Strategy]
			if !ok {
				continue
			}
			ev, ok := cache[t.sub.Strategy]
			if !ok {
				ev = c.eval(data)
				cache[t.sub.Strategy] = ev
			}

			// Bars not evaluated yet: only the newest one on the first run.
			first := last
			if t.sub.LastChecked != 0 {
				first = last + 1
				for first > 0 && candles[first-1].OpenTime.UnixMilli() > t.sub.LastChecked && last-first+1 < MaxCatchUp {
					first--
				}
			}

			stale := staleAfter(p.interval)
			failed := false
			for i := first; i <= last && !failed; i++ {
				if now.Sub(CloseTime(candles[i])) > stale {
					continue // too old to be useful (e.g. after a long outage)
				}
				for _, side := range ev.at(i) {
					sig := Signal{Side: side, Symbol: p.symbol, Interval: p.interval, Strategy: t.sub.Strategy, Candle: candles[i]}
					if err := e.Notify.SendMessage(ctx, t.user.ChatID, FormatSignal(sig, e.Loc)); err != nil {
						errs = append(errs, fmt.Errorf("send to %d: %w", t.user.ChatID, err))
						failed = true
						break
					}
					sent++
				}
			}
			if !failed {
				t.sub.LastChecked = lastMs
			}
		}
	}
	return sent, errors.Join(errs...)
}

// staleAfter is how old a missed candle may be and still produce a signal:
// two candles, but at least 3 hours so an hourly scheduler never drops bars.
func staleAfter(interval string) time.Duration {
	d, _ := market.IntervalDuration(interval)
	if 2*d > 3*time.Hour {
		return 2 * d
	}
	return 3 * time.Hour
}

func minLastChecked[T any](ts []T, get func(T) int64) int64 {
	min := int64(-1)
	for _, t := range ts {
		if v := get(t); min == -1 || v < min {
			min = v
		}
	}
	return min
}

// needsFetch reports whether a candle newer than lastChecked (Unix ms, the
// oldest across subscribers) may have closed. Candles up to 1d are aligned to
// the Unix epoch on Binance; for 3d and 1w we always fetch to be safe.
func needsFetch(interval string, now time.Time, lastChecked int64) bool {
	d, ok := market.IntervalDuration(interval)
	if !ok || d > 24*time.Hour || lastChecked <= 0 {
		return true
	}
	ms := d.Milliseconds()
	lastClosedOpen := (now.UnixMilli()/ms)*ms - ms
	return lastClosedOpen > lastChecked
}

// Backtest evaluates a strategy on recent history and returns the most recent
// signals (newest last), at most max of them, plus the number of bars scanned.
func Backtest(ctx context.Context, m market.Provider, s *store.Strategy, symbol, interval string, now time.Time, max int) ([]Signal, int, error) {
	c, err := CompileStrategy(s)
	if err != nil {
		return nil, 0, err
	}
	candles, err := m.Candles(ctx, symbol, interval, CandleLimit)
	if err != nil {
		return nil, 0, err
	}
	candles = market.ClosedOnly(candles, now)
	ev := c.eval(ToData(candles))
	var sigs []Signal
	for i := range candles {
		for _, side := range ev.at(i) {
			sigs = append(sigs, Signal{Side: side, Symbol: symbol, Interval: interval, Strategy: s.Name, Candle: candles[i]})
		}
	}
	if len(sigs) > max {
		sigs = sigs[len(sigs)-max:]
	}
	return sigs, len(candles), nil
}

// FormatPrice prints a price without unnecessary trailing zeros.
func FormatPrice(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// CloseTime returns the moment the candle closed (Binance reports 1ms before).
func CloseTime(c market.Candle) time.Time {
	return c.CloseTime.Add(time.Millisecond)
}

// FormatSignal renders a signal as a Telegram message.
func FormatSignal(s Signal, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	head := "🟢 سیگنال خرید (BUY)"
	if s.Side == Sell {
		head = "🔴 سیگنال فروش (SELL)"
	}
	return fmt.Sprintf("%s\n\nارز: %s\nتایم‌فریم: %s\nاستراتژی: %s\nقیمت بسته شدن: %s\nبسته شدن کندل: %s",
		head, s.Symbol, s.Interval, s.Strategy, FormatPrice(s.Candle.Close),
		CloseTime(s.Candle).In(loc).Format("2006-01-02 15:04"))
}
