package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
	"github.com/matinnak/crypto-signal-bot/internal/telegram"
)

const admin = 100

type sent struct {
	chat int64
	text string
}

type fakeTG struct{ out []sent }

func (f *fakeTG) SendMessage(_ context.Context, chat int64, text string) error {
	f.out = append(f.out, sent{chat, text})
	return nil
}

func (f *fakeTG) last() string {
	if len(f.out) == 0 {
		return ""
	}
	return f.out[len(f.out)-1].text
}

type fakeMarket struct{}

func (fakeMarket) Candles(_ context.Context, symbol, _ string, _ int) ([]market.Candle, error) {
	if symbol == "NOPEUSDT" {
		return nil, market.ErrInvalidSymbol
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var out []market.Candle
	for i, c := range []float64{1, 2, 3, 2, 4} {
		o := t0.Add(time.Duration(i) * time.Hour)
		out = append(out, market.Candle{OpenTime: o, CloseTime: o.Add(time.Hour - time.Millisecond), Open: c, High: c, Low: c, Close: c})
	}
	return out, nil
}

func newBot() (*Bot, *fakeTG, *store.State) {
	tg := &fakeTG{}
	st := store.New()
	SeedDefaults(st, time.Now())
	b := &Bot{TG: tg, Market: fakeMarket{}, AdminID: admin, Now: func() time.Time { return time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) }}
	return b, tg, st
}

func msg(chat int64, text string) *telegram.Message {
	return &telegram.Message{Chat: telegram.Chat{ID: chat}, From: &telegram.User{ID: chat, FirstName: "Test"}, Text: text}
}

func TestUnknownUserNeedsApproval(t *testing.T) {
	b, tg, st := newBot()
	b.Handle(context.Background(), st, msg(5, "/start"))
	if st.Users[5].Approved {
		t.Fatal("stranger should not be approved")
	}
	if len(tg.out) != 2 || tg.out[1].chat != admin || !strings.Contains(tg.out[1].text, "/approve 5") {
		t.Fatalf("admin was not notified: %+v", tg.out)
	}
	b.Handle(context.Background(), st, msg(admin, "/approve 5"))
	if !st.Users[5].Approved {
		t.Fatal("approve failed")
	}
	b.Handle(context.Background(), st, msg(5, "/new x\nbuy: close > 1"))
	if !strings.Contains(tg.last(), "فقط برای ادمین") {
		t.Fatalf("non-admin created a strategy: %s", tg.last())
	}
}

func TestAddListRemove(t *testing.T) {
	b, tg, st := newBot()
	ctx := context.Background()
	b.Handle(ctx, st, msg(admin, "/add btc/usdt,ETHUSDT,NOPEUSDT 1H,4h ema_cross"))
	if n := len(st.Users[admin].Subs); n != 4 {
		t.Fatalf("expected 4 subs, got %d: %s", n, tg.last())
	}
	if !strings.Contains(tg.last(), "NOPEUSDT") {
		t.Errorf("invalid symbol not reported: %s", tg.last())
	}
	b.Handle(ctx, st, msg(admin, "/add BTCUSDT 1h ema_cross"))
	if len(st.Users[admin].Subs) != 4 || !strings.Contains(tg.last(), "از قبل") {
		t.Errorf("duplicate added: %s", tg.last())
	}
	b.Handle(ctx, st, msg(admin, "/add BTCUSDT 7h ema_cross"))
	if !strings.Contains(tg.last(), "معتبر نیست") {
		t.Errorf("bad interval accepted: %s", tg.last())
	}
	b.Handle(ctx, st, msg(admin, "/list"))
	if !strings.Contains(tg.last(), "1. BTCUSDT | 1h | ema_cross") {
		t.Errorf("list: %s", tg.last())
	}
	b.Handle(ctx, st, msg(admin, "/remove 1,2"))
	if len(st.Users[admin].Subs) != 2 {
		t.Errorf("remove failed: %d left", len(st.Users[admin].Subs))
	}
	b.Handle(ctx, st, msg(admin, "/remove all"))
	if len(st.Users[admin].Subs) != 0 {
		t.Errorf("remove all failed")
	}
}

func TestNewStrategy(t *testing.T) {
	b, tg, st := newBot()
	ctx := context.Background()
	b.Handle(ctx, st, msg(admin, "/new rsi_ema\nbuy: RSI(14) < 30\n  and close > EMA(200)\nsell: RSI(14) > 70\ndesc: test"))
	s, ok := st.Strategies["rsi_ema"]
	if !ok {
		t.Fatalf("not created: %s", tg.last())
	}
	if s.Buy != "RSI(14) < 30 and close > EMA(200)" || s.Description != "test" {
		t.Errorf("bad parse: %+v", s)
	}
	b.Handle(ctx, st, msg(admin, "/new bad\nbuy: RSX(14) < 30"))
	if _, ok := st.Strategies["bad"]; ok || !strings.Contains(tg.last(), "RSX") && !strings.Contains(tg.last(), "rsx") {
		t.Errorf("invalid strategy accepted: %s", tg.last())
	}
	b.Handle(ctx, st, msg(admin, "/new 9lives\nbuy: close > 1"))
	if _, ok := st.Strategies["9lives"]; ok {
		t.Error("numeric-leading name accepted")
	}
}

func TestDeleteStrategyInUse(t *testing.T) {
	b, tg, st := newBot()
	ctx := context.Background()
	b.Handle(ctx, st, msg(admin, "/add BTCUSDT 1h ema_cross"))
	b.Handle(ctx, st, msg(admin, "/delstrategy ema_cross"))
	if _, ok := st.Strategies["ema_cross"]; !ok {
		t.Fatalf("deleted strategy in use: %s", tg.last())
	}
	b.Handle(ctx, st, msg(admin, "/remove all"))
	b.Handle(ctx, st, msg(admin, "/delstrategy ema_cross"))
	if _, ok := st.Strategies["ema_cross"]; ok {
		t.Fatal("not deleted")
	}
}

func TestTestCommand(t *testing.T) {
	b, tg, st := newBot()
	b.Handle(context.Background(), st, msg(admin, "/new up\nbuy: close > close[1]"))
	b.Handle(context.Background(), st, msg(admin, "/test up BTCUSDT 1h"))
	if !strings.Contains(tg.last(), "BUY") || strings.Count(tg.last(), "BUY") != 3 {
		t.Errorf("unexpected backtest output: %s", tg.last())
	}
}

func TestSeededStrategiesCompile(t *testing.T) {
	_, _, st := newBot()
	if len(st.Strategies) < 4 {
		t.Fatalf("expected default strategies")
	}
	for name, s := range st.Strategies {
		if _, err := ParseStrategy(name, "buy: "+s.Buy+"\nsell: "+s.Sell); err != nil {
			t.Errorf("default %s does not compile: %v", name, err)
		}
	}
}
