package main

import (
	"context"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/config"
	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/mtfmacd"
	"github.com/matinnak/crypto-signal-bot/internal/telegram"
)

// fixed runs only the built-in BTCUSDT 3h "MTF MACD Trend-Pullback" strategy.
// Signals go to the admin automatically; any message to the bot gets a status reply.
type fixed struct {
	*app
	runner *mtfmacd.Runner
}

func newFixed(a *app, cfg *config.Config) *fixed {
	return &fixed{app: a, runner: &mtfmacd.Runner{
		Futures: market.NewBinanceFutures(cfg.FuturesURL),
		Spot:    market.NewBinance(cfg.BinanceURL),
		Notify:  a.tg,
		ChatID:  cfg.AdminChatID,
		Loc:     cfg.Location,
		Log:     a.log,
		Params:  mtfmacd.Default(),
	}}
}

func (f *fixed) evaluate(ctx context.Context) *mtfmacd.Snapshot {
	snap, err := f.runner.Evaluate(ctx)
	if err != nil {
		f.log.Printf("evaluate: %v", err)
		return nil
	}
	last := snap.Bars[len(snap.Bars)-1]
	f.log.Printf("data: %s | %d 3h bars | last closed 3h bar opened %s UTC, close %.1f",
		snap.Source, len(snap.Bars), last.OpenTime.Format("2006-01-02 15:04"), last.Close)
	shown := 0
	for i := len(snap.Events) - 1; i >= 0 && shown < 8; i-- {
		e := snap.Events[i]
		if e.Kind == mtfmacd.Signal {
			f.log.Printf("recent signal: %s UTC long=%v entry=%.1f stop=%.1f tp1=%.1f tp2=%.1f",
				e.Bar.OpenTime.Add(mtfmacd.Bar).Format("2006-01-02 15:04"), e.Long, e.Entry, e.Stop, e.TP1, e.TP2)
			shown++
		}
	}
	return snap
}

func (f *fixed) reply(ctx context.Context, ups []telegram.Update, snap *mtfmacd.Snapshot) {
	for _, u := range ups {
		if u.UpdateID >= f.st.UpdateOffset {
			f.st.UpdateOffset = u.UpdateID + 1
		}
		m := u.Message
		if m == nil {
			continue
		}
		text := "🔒 این ربات خصوصی است."
		if m.Chat.ID == f.cfg.AdminChatID {
			text = f.runner.StatusText(snap)
		}
		if err := f.tg.SendMessage(ctx, m.Chat.ID, text); err != nil {
			f.log.Printf("reply to %d: %v", m.Chat.ID, err)
		}
	}
}

func (f *fixed) notify(ctx context.Context, snap *mtfmacd.Snapshot) {
	if snap == nil {
		return
	}
	n, err := f.runner.NotifyNew(ctx, snap, f.st)
	if err != nil {
		f.log.Printf("notify: %v", err)
	}
	f.log.Printf("notifications sent: %d", n)
}

func (f *fixed) runOnce(ctx context.Context) error {
	snap := f.evaluate(ctx)
	for i := 0; i < 20; i++ {
		ups, err := f.tg.GetUpdates(ctx, f.st.UpdateOffset, 0)
		if err != nil {
			f.log.Printf("getUpdates: %v", err)
			break
		}
		if len(ups) == 0 {
			break
		}
		f.reply(ctx, ups, snap)
		if err := f.save(); err != nil {
			return err
		}
	}
	f.notify(ctx, snap)
	return f.save()
}

func (f *fixed) serve(ctx context.Context, every time.Duration) error {
	var snap *mtfmacd.Snapshot
	var last time.Time
	for ctx.Err() == nil {
		if time.Since(last) >= every {
			snap = f.evaluate(ctx)
			f.notify(ctx, snap)
			last = time.Now()
			if err := f.save(); err != nil {
				return err
			}
		}
		ups, err := f.tg.GetUpdates(ctx, f.st.UpdateOffset, 25*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			f.log.Printf("getUpdates: %v", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
			}
			continue
		}
		if len(ups) > 0 {
			f.reply(ctx, ups, snap)
			if err := f.save(); err != nil {
				return err
			}
		}
	}
	return f.save()
}
