package main

import (
	"context"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/config"
	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/signals"
	"github.com/matinnak/crypto-signal-bot/internal/telegram"
)

// fixed runs only the built-in strategies (signals.All): BTC 3h, BNB/SOL/XRP 2h.
// Signals go to the admin automatically; any message to the bot gets a status reply.
type fixed struct {
	*app
	runner *signals.Runner
}

func newFixed(a *app, cfg *config.Config) *fixed {
	return &fixed{app: a, runner: &signals.Runner{
		Specs:   signals.All,
		Futures: market.NewBinanceFutures(cfg.FuturesURL),
		Spot:    market.NewBinance(cfg.BinanceURL),
		Notify:  a.tg,
		ChatID:  cfg.AdminChatID,
		Loc:     cfg.Location,
		Log:     a.log,
	}}
}

func (f *fixed) evaluate(ctx context.Context) []*signals.Snapshot {
	snaps := f.runner.Evaluate(ctx)
	for _, snap := range snaps {
		s := snap.Spec
		if snap.Err != nil {
			f.log.Printf("%s: evaluate failed: %v", s.ID, snap.Err)
			continue
		}
		bars := snap.Result.Bars
		last := bars[len(bars)-1]
		f.log.Printf("%s: %s | %d bars | last closed bar opened %s UTC, close %g",
			s.ID, snap.Source, len(bars), last.OpenTime.Format("2006-01-02 15:04"), last.Close)
		shown := 0
		for i := len(snap.Result.Events) - 1; i >= 0 && shown < 5; i-- {
			e := snap.Result.Events[i]
			if e.Kind == signals.Signal {
				f.log.Printf("%s: recent signal: close %s UTC long=%v entry=%g stop=%g targets=%v",
					s.ID, e.Bar.OpenTime.Add(s.Bar).Format("2006-01-02 15:04"), e.Long, e.Entry, e.Stop, e.Targets)
				shown++
			}
		}
	}
	return snaps
}

func (f *fixed) reply(ctx context.Context, ups []telegram.Update, snap []*signals.Snapshot) {
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

func (f *fixed) notify(ctx context.Context, snap []*signals.Snapshot) {
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
	var snap []*signals.Snapshot
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
