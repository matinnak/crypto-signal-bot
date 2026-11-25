// Package config reads settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed time zones so TIMEZONE works on minimal systems
)

// Config holds runtime settings.
type Config struct {
	TelegramToken string
	AdminChatID   int64
	StateKey      string // encrypts the state file when set
	BinanceURL    string
	TelegramURL   string // override for tests; default https://api.telegram.org
	Location      *time.Location
	OpenAccess    bool
	OnCI          bool   // running inside GitHub Actions
	Strategy      string // "mtf_macd" = run only the built-in BTC 3h strategy
	FuturesURL    string // Binance USDT-M futures endpoint (fixed strategy)
}

// FromEnv loads the configuration.
//
//	TELEGRAM_TOKEN    required, from @BotFather
//	ADMIN_CHAT_ID     required, your numeric Telegram chat id
//	STATE_KEY         passphrase used to encrypt the state file (required on GitHub Actions)
//	BINANCE_BASE_URL  optional, default https://data-api.binance.vision
//	TIMEZONE          optional, default Asia/Tehran
//	OPEN_ACCESS       optional, "true" lets anyone use the bot without approval
//	STRATEGY          optional, "mtf_macd" runs only the built-in BTCUSDT 3h strategy
func FromEnv() (*Config, error) {
	c := &Config{
		TelegramToken: strings.TrimSpace(os.Getenv("TELEGRAM_TOKEN")),
		StateKey:      os.Getenv("STATE_KEY"),
		BinanceURL:    strings.TrimSpace(os.Getenv("BINANCE_BASE_URL")),
		TelegramURL:   strings.TrimSpace(os.Getenv("TELEGRAM_API_URL")),
		OpenAccess:    strings.EqualFold(os.Getenv("OPEN_ACCESS"), "true"),
		OnCI:          os.Getenv("GITHUB_ACTIONS") == "true",
		Strategy:      strings.ToLower(strings.TrimSpace(os.Getenv("STRATEGY"))),
		FuturesURL:    strings.TrimSpace(os.Getenv("BINANCE_FUTURES_URL")),
	}
	var errs []error
	if c.TelegramToken == "" {
		errs = append(errs, errors.New("TELEGRAM_TOKEN is not set"))
	}
	if v := strings.TrimSpace(os.Getenv("ADMIN_CHAT_ID")); v == "" {
		errs = append(errs, errors.New("ADMIN_CHAT_ID is not set"))
	} else if id, err := strconv.ParseInt(v, 10, 64); err != nil {
		errs = append(errs, fmt.Errorf("ADMIN_CHAT_ID must be a number, got %q", v))
	} else {
		c.AdminChatID = id
	}
	if c.Strategy != "" && c.Strategy != "mtf_macd" {
		errs = append(errs, fmt.Errorf("unknown STRATEGY %q (supported: mtf_macd)", c.Strategy))
	}
	if c.OnCI && c.StateKey == "" {
		errs = append(errs, errors.New("STATE_KEY must be set on GitHub Actions so the saved state is encrypted"))
	}
	tz := strings.TrimSpace(os.Getenv("TIMEZONE"))
	if tz == "" {
		tz = "Asia/Tehran"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		errs = append(errs, fmt.Errorf("TIMEZONE %q: %w", tz, err))
	}
	c.Location = loc
	return c, errors.Join(errs...)
}
