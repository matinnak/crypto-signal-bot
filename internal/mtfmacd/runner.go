package mtfmacd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
)

// Symbol is the only market this strategy trades.
const Symbol = "BTCUSDT"

// Bar is the chart timeframe.
const Bar = 3 * time.Hour

// StaleAfter: events older than this (bar close) are never notified, so the
// first run or a run after an outage does not flood old signals.
const StaleAfter = 7 * time.Hour

// Notifier sends a message.
type Notifier interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Runner fetches data, runs the strategy and notifies new events.
type Runner struct {
	Futures market.Provider // primary source (BTCUSDT.P)
	Spot    market.Provider // fallback when futures data is not reachable
	Notify  Notifier
	ChatID  int64
	Loc     *time.Location
	Now     func() time.Time
	Log     *log.Logger
	Params  Params
}

// Snapshot is one evaluation of the strategy.
type Snapshot struct {
	Result
	Source string
	At     time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) loc() *time.Location {
	if r.Loc == nil {
		return time.UTC
	}
	return r.Loc
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Printf(format, args...)
	}
}

func fetchAll(ctx context.Context, m market.Provider, now time.Time) (h1, h4, d1 []market.Candle, err error) {
	if h1, err = m.Candles(ctx, Symbol, "1h", 1000); err != nil {
		return
	}
	if h4, err = m.Candles(ctx, Symbol, "4h", 1000); err != nil {
		return
	}
	if d1, err = m.Candles(ctx, Symbol, "1d", 300); err != nil {
		return
	}
	return market.ClosedOnly(h1, now), market.ClosedOnly(h4, now), market.ClosedOnly(d1, now), nil
}

// Evaluate downloads candles (futures first, spot as fallback) and runs the strategy.
func (r *Runner) Evaluate(ctx context.Context) (*Snapshot, error) {
	now := r.now()
	source := "Binance Futures (BTCUSDT.P)"
	h1, h4, d1, err := fetchAll(ctx, r.Futures, now)
	if err != nil && r.Spot != nil {
		r.logf("futures data unavailable (%v); using spot", err)
		source = "Binance Spot (BTCUSDT) — دیتای فیوچرز در دسترس نبود"
		h1, h4, d1, err = fetchAll(ctx, r.Spot, now)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch candles: %w", err)
	}
	bars := market.Aggregate(h1, Bar)
	if len(bars) < 60 {
		return nil, fmt.Errorf("not enough 3h candles: %d", len(bars))
	}
	res := Run(r.Params, bars, h4, d1)
	return &Snapshot{Result: res, Source: source, At: now}, nil
}

func eventKey(e Event) string {
	return string(e.Kind) + ":" + strconv.FormatInt(e.Bar.OpenTime.UnixMilli(), 10)
}

// NotifyNew sends events that are recent and not yet sent, recording them in st.
func (r *Runner) NotifyNew(ctx context.Context, snap *Snapshot, st *store.State) (int, error) {
	if st.Notified == nil {
		st.Notified = map[string]int64{}
	}
	sent := 0
	var errs []error
	for _, e := range snap.Events {
		closeAt := e.Bar.OpenTime.Add(Bar)
		if snap.At.Sub(closeAt) > StaleAfter {
			continue
		}
		key := eventKey(e)
		if _, done := st.Notified[key]; done {
			continue
		}
		if err := r.Notify.SendMessage(ctx, r.ChatID, r.FormatEvent(e, snap.Source)); err != nil {
			errs = append(errs, err)
			continue
		}
		st.Notified[key] = snap.At.Unix()
		sent++
	}
	// Forget keys older than 60 days.
	for k, ts := range st.Notified {
		if snap.At.Unix()-ts > 60*24*3600 {
			delete(st.Notified, k)
		}
	}
	return sent, errors.Join(errs...)
}

// --- formatting ------------------------------------------------------------

// Price formats a BTC price like 95,210.5.
func Price(v float64) string {
	if math.IsNaN(v) {
		return "—"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', 1, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	out := b.String() + "." + frac
	if v < 0 {
		out = "-" + out
	}
	return out
}

func pct(a, b float64) string {
	return strconv.FormatFloat(math.Abs(a-b)/b*100, 'f', 2, 64) + "%"
}

func dirName(long bool) string {
	if long {
		return "LONG 🟢"
	}
	return "SHORT 🔴"
}

func (r *Runner) barClose(b market.Candle) string {
	return b.OpenTime.Add(Bar).In(r.loc()).Format("2006-01-02 15:04")
}

// FormatEvent renders an event as a Telegram message.
func (r *Runner) FormatEvent(e Event, source string) string {
	p := r.Params
	switch e.Kind {
	case Signal:
		trend := "صعودی (close > EMA" + strconv.Itoa(p.HTFEmaLen) + ")"
		inv := "اگر کندل بعدی زیر حد ضرر باز شد، وارد نشوید."
		if !e.Long {
			trend = "نزولی (close < EMA" + strconv.Itoa(p.HTFEmaLen) + ")"
			inv = "اگر کندل بعدی بالای حد ضرر باز شد، وارد نشوید."
		}
		strength := "معمولی"
		if e.Strong {
			strength = "بالا (روزانه هم‌جهت با EMA" + strconv.Itoa(p.StrengthEmaLen) + ")"
		}
		return fmt.Sprintf(`📣 سیگنال %s — %s
استراتژی: MTF MACD Trend-Pullback v1.0 | تایم‌فریم 3h

ورود: ~%s (مارکت، باز شدن کندل بعد)
حد ضرر: %s  (فاصله %s)
تارگت ۱: %s  (%gR — بستن %g%%)
تارگت ۲: %s  (%gR — بستن %g%%)
بعد از تارگت ۱، حد ضرر به نقطه‌ی ورود منتقل می‌شود.

روند 4h: %s
قدرت سیگنال: %s
ریسک پیشنهادی: %g%% سرمایه در هر معامله
بسته شدن کندل: %s
داده: %s

⚠️ ابطال: %s
⚠️ این استراتژی در آزمون خارج از نمونه تأیید نشده — فقط برای معامله‌ی آزمایشی.`,
			dirName(e.Long), Symbol,
			Price(e.Entry), Price(e.Stop), pct(e.Entry, e.Stop),
			Price(e.TP1), p.TP1R, p.TP1Pct, Price(e.TP2), p.TP2R, p.TP2Pct,
			trend, strength, p.RiskPct, r.barClose(e.Bar), source, inv)

	case TP1:
		return fmt.Sprintf("✅ تارگت ۱ خورد — %s %s\nحد ضرر به %s (نقطه‌ی ورود) منتقل شد.\nتارگت ۲: %s\nبسته شدن کندل: %s",
			Symbol, dirName(e.Long), Price(e.NewStop), Price(e.TP2), r.barClose(e.Bar))

	case Exit:
		reason := map[string]string{
			"tp2":       "🎯 تارگت ۲ خورد",
			"stop":      "🛑 حد ضرر خورد",
			"breakeven": "⚖️ بعد از تارگت ۱ در نقطه‌ی ورود بسته شد",
		}[e.ExitReason]
		if reason == "" {
			reason = "بسته شد"
		}
		return fmt.Sprintf("🏁 معامله‌ی %s %s بسته شد\n%s\nنتیجه: %+.2fR (بدون کارمزد)\nبسته شدن کندل: %s",
			dirName(e.Long), Symbol, reason, e.ResultR, r.barClose(e.Bar))
	}
	return ""
}

// StatusText describes the current state, for /start or /status.
func (r *Runner) StatusText(snap *Snapshot) string {
	var b strings.Builder
	b.WriteString("🤖 ربات سیگنال — MTF MACD Trend-Pullback v1.0\n")
	b.WriteString("BTCUSDT | تایم‌فریم 3h | روند 4h\n\n")
	b.WriteString("ربات هر ساعت چارت را بررسی می‌کند و سیگنال‌ها را خودکار می‌فرستد؛ لازم نیست کاری بکنید.\n\n")
	if snap == nil {
		b.WriteString("⚠️ دریافت داده از صرافی ناموفق بود؛ در اجرای بعدی دوباره امتحان می‌شود.")
		return b.String()
	}
	last := snap.Bars[len(snap.Bars)-1]
	fmt.Fprintf(&b, "آخرین کندل بسته‌شده: %s (قیمت %s)\n", r.barClose(last), Price(last.Close))
	fmt.Fprintf(&b, "داده: %s\n\n", snap.Source)

	switch pos := snap.Position; {
	case pos == nil:
		b.WriteString("وضعیت: بدون معامله‌ی باز — منتظر سیگنال بعدی\n")
	case pos.PendingFill:
		fmt.Fprintf(&b, "وضعیت: سیگنال %s — ورود در باز شدن کندل بعد\nحد ضرر %s | تارگت ۱ %s | تارگت ۲ %s\n",
			dirName(pos.Long), Price(pos.Stop), Price(pos.TP1), Price(pos.TP2))
	default:
		tp1 := ""
		if pos.TP1Done {
			tp1 = " (تارگت ۱ خورده)"
		}
		fmt.Fprintf(&b, "وضعیت: معامله‌ی باز %s%s\nورود %s | حد ضرر %s | تارگت ۱ %s | تارگت ۲ %s\n",
			dirName(pos.Long), tp1, Price(pos.Entry), Price(pos.Stop), Price(pos.TP1), Price(pos.TP2))
	}

	var sigs []Event
	for _, e := range snap.Events {
		if e.Kind == Signal {
			sigs = append(sigs, e)
		}
	}
	sort.SliceStable(sigs, func(i, j int) bool { return sigs[i].Bar.OpenTime.Before(sigs[j].Bar.OpenTime) })
	if len(sigs) > 6 {
		sigs = sigs[len(sigs)-6:]
	}
	if len(sigs) > 0 {
		b.WriteString("\nسیگنال‌های اخیر (برای مقایسه با TradingView):\n")
		for _, e := range sigs {
			d := "LONG "
			if !e.Long {
				d = "SHORT"
			}
			fmt.Fprintf(&b, "• %s  %s  ورود %s  SL %s\n", r.barClose(e.Bar), d, Price(e.Entry), Price(e.Stop))
		}
	}
	return b.String()
}
