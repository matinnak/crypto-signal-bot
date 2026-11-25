package market

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const sampleKlines = `[
 [1700000000000,"100.5","101.0","99.5","100.8","12.3",1700003599999,"0",1,"0","0","0"],
 [1700003600000,"100.8","102.0","100.1","101.9","8.1",1700007199999,"0",1,"0","0","0"]
]`

func TestBinanceCandles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" || r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(sampleKlines))
	}))
	defer srv.Close()

	c, err := NewBinance(srv.URL).Candles(context.Background(), "BTCUSDT", "1h", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || c[1].Close != 101.9 || c[0].Volume != 12.3 {
		t.Fatalf("bad candles: %+v", c)
	}
	if !c[0].OpenTime.Equal(time.UnixMilli(1700000000000)) {
		t.Errorf("bad open time %v", c[0].OpenTime)
	}
}

func TestBinanceInvalidSymbol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
	}))
	defer srv.Close()

	_, err := NewBinance(srv.URL).Candles(context.Background(), "NOPE", "1h", 2)
	if !errors.Is(err, ErrInvalidSymbol) {
		t.Fatalf("expected ErrInvalidSymbol, got %v", err)
	}
}

func TestClosedOnly(t *testing.T) {
	now := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	c := []Candle{
		{CloseTime: now.Add(-time.Hour)},
		{CloseTime: now.Add(30 * time.Minute)}, // still open
	}
	if got := ClosedOnly(c, now); len(got) != 1 {
		t.Fatalf("expected 1 closed candle, got %d", len(got))
	}
}

func TestNormalize(t *testing.T) {
	if NormalizeSymbol(" btc/usdt ") != "BTCUSDT" {
		t.Error("symbol")
	}
	for in, want := range map[string]string{"1H": "1h", "4h": "4h", "15m": "15m", "1D": "1d"} {
		if got := NormalizeInterval(in); got != want {
			t.Errorf("NormalizeInterval(%q)=%q want %q", in, got, want)
		}
	}
}
