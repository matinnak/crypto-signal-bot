// Command signalbot watches crypto symbols for strategy signals and reports
// them on Telegram.
//
// Two modes:
//
//	-mode once   process pending Telegram messages, check signals, save, exit
//	             (meant for a scheduler such as GitHub Actions cron)
//	-mode serve  run forever: answer messages instantly and check every -every
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/bot"
	"github.com/matinnak/crypto-signal-bot/internal/config"
	"github.com/matinnak/crypto-signal-bot/internal/engine"
	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
	"github.com/matinnak/crypto-signal-bot/internal/telegram"
)

type app struct {
	cfg       *config.Config
	statePath string
	st        *store.State
	tg        *telegram.Client
	bot       *bot.Bot
	engine    *engine.Engine
	log       *log.Logger
}

func main() {
	mode := flag.String("mode", "once", "once or serve")
	statePath := flag.String("state", "state.json", "path of the state file")
	every := flag.Duration("every", time.Minute, "serve mode: how often to check for signals")
	flag.Parse()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Fatalf("config: %v", err)
	}

	st, err := store.Load(*statePath, cfg.StateKey)
	if err != nil {
		logger.Fatalf("load state: %v", err)
	}
	bot.SeedDefaults(st, time.Now())

	tg := telegram.New(cfg.TelegramToken)
	if cfg.TelegramURL != "" {
		tg.BaseURL = cfg.TelegramURL
	}
	mkt := market.NewBinance(cfg.BinanceURL)
	a := &app{
		cfg:       cfg,
		statePath: *statePath,
		st:        st,
		tg:        tg,
		log:       logger,
		bot:       &bot.Bot{TG: tg, Market: mkt, AdminID: cfg.AdminChatID, OpenAccess: cfg.OpenAccess, Loc: cfg.Location, Log: logger},
		engine:    &engine.Engine{Market: mkt, Notify: tg, Loc: cfg.Location, Workers: 4, Log: logger},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Strategy == "builtin" {
		f := newFixed(a, cfg)
		switch *mode {
		case "once":
			err = f.runOnce(ctx)
		case "serve":
			err = f.serve(ctx, *every)
		default:
			logger.Fatalf("unknown -mode %q (use once or serve)", *mode)
		}
		if err != nil {
			logger.Fatal(err)
		}
		return
	}

	switch *mode {
	case "once":
		err = a.runOnce(ctx)
	case "serve":
		err = a.serve(ctx, *every)
	default:
		logger.Fatalf("unknown -mode %q (use once or serve)", *mode)
	}
	if err != nil {
		logger.Fatal(err)
	}
}

func (a *app) save() error {
	return store.Save(a.statePath, a.cfg.StateKey, a.st)
}

// handleUpdates processes a batch of updates and advances the offset.
func (a *app) handleUpdates(ctx context.Context, ups []telegram.Update) {
	for _, u := range ups {
		if u.Message != nil {
			a.bot.Handle(ctx, a.st, u.Message)
		}
		if u.UpdateID >= a.st.UpdateOffset {
			a.st.UpdateOffset = u.UpdateID + 1
		}
	}
}

func (a *app) check(ctx context.Context) {
	start := time.Now()
	n, err := a.engine.Check(ctx, a.st)
	if err != nil {
		a.log.Printf("check finished with errors: %v", err)
	}
	a.log.Printf("check done: %d signal(s) sent in %s", n, time.Since(start).Round(time.Millisecond))
}

func (a *app) runOnce(ctx context.Context) error {
	// Drain all pending messages (Telegram keeps them for 24 hours).
	for i := 0; i < 20; i++ {
		ups, err := a.tg.GetUpdates(ctx, a.st.UpdateOffset, 0)
		if err != nil {
			a.log.Printf("getUpdates: %v", err)
			break
		}
		if len(ups) == 0 {
			break
		}
		a.log.Printf("handling %d message(s)", len(ups))
		a.handleUpdates(ctx, ups)
		if err := a.save(); err != nil {
			return err
		}
	}
	a.check(ctx)
	return a.save()
}

func (a *app) serve(ctx context.Context, every time.Duration) error {
	a.log.Printf("serving; checking signals every %s", every)
	lastCheck := time.Time{}
	for ctx.Err() == nil {
		if time.Since(lastCheck) >= every {
			a.check(ctx)
			lastCheck = time.Now()
			if err := a.save(); err != nil {
				return err
			}
		}

		// Long-poll for at most the time left until the next check.
		wait := every - time.Since(lastCheck)
		if wait > 25*time.Second {
			wait = 25 * time.Second
		}
		if wait < time.Second {
			wait = time.Second
		}
		ups, err := a.tg.GetUpdates(ctx, a.st.UpdateOffset, wait)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			a.log.Printf("getUpdates: %v", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
			}
			continue
		}
		if len(ups) > 0 {
			a.handleUpdates(ctx, ups)
			if err := a.save(); err != nil {
				return err
			}
		}
	}
	a.log.Print("shutting down")
	return a.save()
}
