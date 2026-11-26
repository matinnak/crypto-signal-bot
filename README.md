# Crypto Signal Bot

A Telegram bot, written in Go with **only the standard library**, that watches crypto
symbols and sends a message whenever a trading strategy fires on a closed candle.

Strategies are written in a small **Pine-like condition language** and can be created,
tested and assigned to symbols entirely from Telegram:

```
/new rsi_ema
buy:  RSI(14) < 30 and close > EMA(200)
sell: RSI(14) > 70
desc: Oversold entries in an uptrend

/test rsi_ema BTCUSDT 4h                     → last signals on history, to compare with TradingView
/add BTCUSDT,ETHUSDT 1h,4h rsi_ema           → watch 2 symbols × 2 timeframes
```

```
🟢 سیگنال خرید (BUY)

ارز: BTCUSDT
تایم‌فریم: 4h
استراتژی: rsi_ema
قیمت بسته شدن: 95200.5
بسته شدن کندل: 2026-11-25 15:30
```

> 🇮🇷 راهنمای فارسی راه‌اندازی: [docs/SETUP_FA.md](docs/SETUP_FA.md)

## Built-in strategies

With `STRATEGY=builtin` (the default in the GitHub Actions workflow) the bot runs only Go ports
of five Pine Script strategies (`internal/signals`) and pushes their signals to the admin:

| Symbol | TF | Strategy | Entry | Stop | Target |
|---|---|---|---|---|---|
| BTCUSDT | 3h | MTF MACD Trend-Pullback | MACD cross with 4h EMA50 trend, market | 5-bar swing ∓ 0.25×ATR | 1R (50%) + 2R (50%), BE after TP1 |
| BNBUSDT | 2h | BOS Retest | close beyond a 5/2 swing, **limit** at the level (6 bars) | opposite swing, ATR-bounded | 2.5R |
| SOLUSDT | 2h | Stoch Reversion | Stoch(14,3,3) %K/%D cross out of 20/80 while ADX(14) < 20, market | ∓ 1.5×ATR | 1.5R |
| XRPUSDT | 2h | MACD Momentum | MACD(12,26,9) cross, market | ∓ 2×ATR | 2.5R |
| ETHUSDT (spot) | 1h | BB Breakout Momentum | Z-score(25) beyond ±2.75, market | ∓ 3×ATR | 6×ATR (2R), time exit after 72 bars |

- 3h candles are built from 1h candles, aligned like TradingView. Binance USDT-M futures data
  (`.P`) is used when reachable, spot otherwise.
- One order or position at a time and daily/weekly loss guards: orders and positions are simulated
  bar by bar with TradingView's broker-emulator rules (next-open fills, limit fills along the
  intrabar path, gaps, partial targets, break-even) so that signals match the chart.
- Messages: new signal (entry, stop, targets), limit filled / cancelled, TP1 + break-even, trade
  closed (result in R). Any message to the bot returns the status of every strategy.

> The scripts' own notes say none of them is validated out-of-sample (BNB was rejected); use them
> for paper trading.

## Features

- **Condition language** with a hand-written lexer, recursive-descent parser, type checker
  and vectorised evaluator (`internal/dsl`), with clear error messages pointing at the
  offending character.
- **TradingView-compatible indicators** (`internal/indicator`): SMA, EMA, RMA, WMA, RSI,
  MACD, Bollinger Bands, ATR, Stochastic, ADX/DMI, highest/lowest, crossover/crossunder.
  Warm-up and `na` semantics follow Pine's `ta.*` functions.
- **Multi-user** with admin approval, per-user watchlists and a shared strategy library.
- **Concurrent market-data fetching** with a worker pool; each symbol/timeframe pair is
  downloaded once per check no matter how many users watch it, and skipped entirely when no
  new candle has closed.
- **No duplicate or missed signals**: every subscription remembers the last evaluated
  candle and catches up on candles missed by a late run.
- **Two deployment modes**
  - `-mode once` – process pending messages, check, save, exit. Runs **free on GitHub
    Actions** every hour; state is **AES-256-GCM encrypted** and kept on a separate `state`
    branch, so the repository can stay public.
  - `-mode serve` – long-running process with instant replies (Docker image included).

## Architecture

```
                 ┌──────────────────────── signalbot ────────────────────────┐
 Telegram  ◄────►│ telegram  ──►  bot (commands, access control)             │
                 │                   │                                       │
                 │                   ▼                                       │
                 │               store (JSON state, AES-GCM)                 │
                 │                   ▲                                       │
                 │                   │                                       │
 Binance   ◄─────│ market  ──►  engine ──► dsl ──► indicator                 │
 (klines)        │          (worker pool, dedup, catch-up)                   │
                 └───────────────────────────────────────────────────────────┘
```

| Package | Responsibility |
|---|---|
| `cmd/signalbot` | Wiring, `once` / `serve` modes, graceful shutdown |
| `internal/config` | Environment variables |
| `internal/telegram` | Minimal Bot API client (`getUpdates`, `sendMessage`, message splitting, rate-limit retry) |
| `internal/market` | Candle model, Binance REST client |
| `internal/indicator` | Technical indicators on `[]float64` |
| `internal/dsl` | Lexer, parser, type checker and evaluator of the condition language |
| `internal/engine` | Fetch, evaluate, de-duplicate and send signals; backtest |
| `internal/bot` | Telegram commands |
| `internal/store` | Persistent state with optional encryption |

## Condition language

```
expr    = or
or      = and { "or" and }
and     = not { "and" not }
not     = "not" not | cmp
cmp     = add [ ("<" | ">" | "<=" | ">=" | "==" | "!=") add ]
add     = mul { ("+" | "-") mul }
mul     = unary { ("*" | "/") unary }
unary   = "-" unary | postfix
postfix = primary { "[" INT "]" }          // history: close[1] = previous close
primary = NUMBER | IDENT | IDENT "(" args ")" | "(" expr ")"
```

- Series: `open high low close volume hl2 hlc3 ohlc4`
- Functions take an optional source first (defaults to `close`): `EMA(200)` = `EMA(close, 200)`
- Names are case-insensitive; `&&`, `||`, `!` are accepted too.
- A condition must evaluate to true/false; comparisons with `na` are false, as in Pine.

Examples:

```
crossover(EMA(9), EMA(21))
crossover(MACD_line, MACD_signal) and close > EMA(200)
close < BB_lower(20, 2) and RSI(14) < 35
volume > SMA(volume, 20) * 1.5
close > highest(high, 20)[1]
ADX(14, 14) > 25 and PLUS_DI(14) > MINUS_DI(14)
```

Send `/indicators` to the bot for the full list.

## Commands

| Command | Who | Description |
|---|---|---|
| `/strategies` | all | List strategies with their numbers |
| `/add SYMBOLS TFS STRATEGY` | all | e.g. `/add BTCUSDT,ETHUSDT 1h,4h 2` |
| `/list`, `/remove N` / `/remove all` | all | Manage your watchlist |
| `/test STRATEGY SYMBOL TF` | all | Show the latest signals on history |
| `/indicators` | all | Language reference |
| `/new NAME` + `buy:` / `sell:` / `desc:` lines | admin | Create or update a strategy |
| `/delstrategy NAME` | admin | Delete an unused strategy |
| `/users`, `/approve ID`, `/revoke ID` | admin | Access control |

## Configuration

| Variable | Required | Description |
|---|---|---|
| `TELEGRAM_TOKEN` | ✅ | Bot token from @BotFather |
| `ADMIN_CHAT_ID` | ✅ | Your numeric chat id |
| `STATE_KEY` | on CI | Passphrase that encrypts the state file |
| `TIMEZONE` | | Time zone for messages (default `Asia/Tehran`) |
| `BINANCE_BASE_URL` | | Default `https://data-api.binance.vision` (spot market data) |
| `OPEN_ACCESS` | | `true` = anyone can use the bot without approval |

## Running

**GitHub Actions (free, hourly)** – add `TELEGRAM_TOKEN`, `ADMIN_CHAT_ID` and `STATE_KEY`
as repository secrets; `.github/workflows/run.yml` does the rest. Use *Actions → Signal bot
→ Run workflow* for an immediate run. Note: in hourly mode the bot answers commands at the
next run.

**Locally**

```bash
export TELEGRAM_TOKEN=... ADMIN_CHAT_ID=...
go run ./cmd/signalbot -mode serve            # instant replies, checks every minute
go run ./cmd/signalbot -mode once             # single pass
```

**Docker**

```bash
docker build -t signalbot .
docker run -d --restart=always -e TELEGRAM_TOKEN=... -e ADMIN_CHAT_ID=... -v signalbot:/data signalbot
```

## Development

```bash
go test -race -cover ./...
go vet ./...
```

## Notes and limitations

- Only **closed** candles are evaluated, so signals never repaint.
- Recursive indicators (EMA, RSI, ...) are computed on the last 1000 candles; long
  periods such as EMA(200) can differ slightly from TradingView, which uses full history.
- The condition language covers indicator conditions, not full Pine Script (no `var`,
  loops or `strategy.*`). Complex logic can be added as a Go function in `internal/dsl/funcs.go`.
- Market data: Binance **spot**. Another exchange only needs a new `market.Provider`.
- This is a notification tool, not financial advice.
