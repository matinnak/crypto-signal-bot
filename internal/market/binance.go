package market

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBinanceURL is Binance's public market-data endpoint. Unlike
// api.binance.com it is reachable from most regions (including GitHub's
// US-hosted runners) and needs no API key.
const DefaultBinanceURL = "https://data-api.binance.vision"

// DefaultFuturesURL is the Binance USDT-M futures REST endpoint (BTCUSDT.P on TradingView).
const DefaultFuturesURL = "https://fapi.binance.com"

// Binance fetches candles from the Binance REST API (spot or USDT-M futures).
type Binance struct {
	BaseURL string
	Path    string // "/api/v3/klines" (spot) or "/fapi/v1/klines" (futures)
	HTTP    *http.Client
}

// NewBinance returns a client for baseURL (DefaultBinanceURL when empty).
func NewBinance(baseURL string) *Binance {
	if baseURL == "" {
		baseURL = DefaultBinanceURL
	}
	return &Binance{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Path:    "/api/v3/klines",
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

// NewBinanceFutures returns a client for USDT-M perpetual futures candles.
func NewBinanceFutures(baseURL string) *Binance {
	if baseURL == "" {
		baseURL = DefaultFuturesURL
	}
	b := NewBinance(baseURL)
	b.Path = "/fapi/v1/klines"
	return b
}

type binanceError struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// Candles implements Provider. limit is capped at 1000 by Binance.
func (b *Binance) Candles(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))
	path := b.Path
	if path == "" {
		path = "/api/v3/klines"
	}
	u := b.BaseURL + path + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("binance read: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var be binanceError
		if json.Unmarshal(body, &be) == nil && be.Code == -1121 {
			return nil, fmt.Errorf("%w: %s", ErrInvalidSymbol, symbol)
		}
		return nil, fmt.Errorf("binance: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return parseKlines(body)
}

// parseKlines decodes Binance's array-of-arrays kline format:
// [openTime, "open", "high", "low", "close", "volume", closeTime, ...]
func parseKlines(body []byte) ([]Candle, error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("binance decode: %w", err)
	}
	out := make([]Candle, 0, len(rows))
	for i, r := range rows {
		if len(r) < 7 {
			return nil, fmt.Errorf("binance decode: row %d has %d fields", i, len(r))
		}
		var openMs, closeMs int64
		if err := json.Unmarshal(r[0], &openMs); err != nil {
			return nil, fmt.Errorf("binance decode open time: %w", err)
		}
		if err := json.Unmarshal(r[6], &closeMs); err != nil {
			return nil, fmt.Errorf("binance decode close time: %w", err)
		}
		var vals [5]float64
		for j := 0; j < 5; j++ {
			var s string
			if err := json.Unmarshal(r[j+1], &s); err != nil {
				return nil, fmt.Errorf("binance decode field %d: %w", j+1, err)
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("binance parse field %d: %w", j+1, err)
			}
			vals[j] = f
		}
		out = append(out, Candle{
			OpenTime:  time.UnixMilli(openMs).UTC(),
			CloseTime: time.UnixMilli(closeMs).UTC(),
			Open:      vals[0],
			High:      vals[1],
			Low:       vals[2],
			Close:     vals[3],
			Volume:    vals[4],
		})
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
