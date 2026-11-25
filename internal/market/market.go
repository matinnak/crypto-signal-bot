// Package market fetches OHLCV candles from exchanges.
package market

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Candle is a single OHLCV bar.
type Candle struct {
	OpenTime  time.Time
	CloseTime time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// Provider is anything that can return candles for a symbol and interval.
// Candles are returned oldest first.
type Provider interface {
	Candles(ctx context.Context, symbol, interval string, limit int) ([]Candle, error)
}

// ErrInvalidSymbol is returned when the exchange does not know the symbol.
var ErrInvalidSymbol = errors.New("invalid symbol")

// Intervals supported by Binance, mapped to their duration.
var intervals = map[string]time.Duration{
	"1m":  time.Minute,
	"3m":  3 * time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"2h":  2 * time.Hour,
	"4h":  4 * time.Hour,
	"6h":  6 * time.Hour,
	"8h":  8 * time.Hour,
	"12h": 12 * time.Hour,
	"1d":  24 * time.Hour,
	"3d":  72 * time.Hour,
	"1w":  7 * 24 * time.Hour,
}

// ValidInterval reports whether s is a supported timeframe.
func ValidInterval(s string) bool {
	_, ok := intervals[s]
	return ok
}

// IntervalDuration returns the length of one candle of the given timeframe.
func IntervalDuration(s string) (time.Duration, bool) {
	d, ok := intervals[s]
	return d, ok
}

// NormalizeInterval converts user input like "1H" or "4h" to the exchange form.
// Minutes keep the lower-case "m"; "1M" (month) is not supported.
func NormalizeInterval(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "m") {
		return s
	}
	return strings.ToLower(s)
}

// SupportedIntervals returns the list of timeframes in a stable order.
func SupportedIntervals() []string {
	return []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w"}
}

// NormalizeSymbol turns "btc/usdt" or "BTC-USDT" into "BTCUSDT".
func NormalizeSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer("/", "", "-", "", "_", "").Replace(s)
	return s
}

// ClosedOnly drops candles that have not closed yet at time now.
func ClosedOnly(c []Candle, now time.Time) []Candle {
	for len(c) > 0 && !c[len(c)-1].CloseTime.Before(now) {
		c = c[:len(c)-1]
	}
	return c
}

// Aggregate combines consecutive candles into larger ones of length d, aligned
// to the Unix epoch (so 3h candles open at 00:00, 03:00, ... UTC, like
// TradingView). Only complete groups are returned; src must be closed candles
// of one interval, oldest first.
func Aggregate(src []Candle, d time.Duration) []Candle {
	if len(src) == 0 {
		return nil
	}
	step := src[0].CloseTime.Sub(src[0].OpenTime) + time.Millisecond
	need := int(d / step)
	if need < 1 {
		return nil
	}
	ms := d.Milliseconds()
	var out []Candle
	var cur Candle
	count := 0
	curKey := int64(-1)
	flush := func() {
		if count == need {
			cur.CloseTime = cur.OpenTime.Add(d - time.Millisecond)
			out = append(out, cur)
		}
	}
	for _, c := range src {
		key := c.OpenTime.UnixMilli() / ms
		if key != curKey {
			flush()
			curKey = key
			cur = Candle{OpenTime: time.UnixMilli(key * ms).UTC(), Open: c.Open, High: c.High, Low: c.Low}
			count = 0
		}
		if c.High > cur.High {
			cur.High = c.High
		}
		if c.Low < cur.Low {
			cur.Low = c.Low
		}
		cur.Close = c.Close
		cur.Volume += c.Volume
		count++
	}
	flush()
	return out
}
