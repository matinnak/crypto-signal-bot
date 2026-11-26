package signals

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
)

// Notifier sends a message.
type Notifier interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Runner evaluates all strategies and notifies new events.
type Runner struct {
	Specs   []*Spec
	Futures market.Provider // primary source (USDT-M perpetuals, as in the scripts)
	Spot    market.Provider // fallback when futures data is not reachable
	Notify  Notifier
	ChatID  int64
	Loc     *time.Location
	Now     func() time.Time
	Log     *log.Logger
}

// Snapshot is one evaluation of one strategy.
type Snapshot struct {
	Spec   *Spec
	Result Result
	Source string
	At     time.Time
	Err    error
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

func fetch(ctx context.Context, m market.Provider, s *Spec, now time.Time) (bars, h4, d1 []market.Candle, err error) {
	if bars, err = m.Candles(ctx, s.Symbol, s.Interval, 1000); err != nil {
		return
	}
	if s.NeedH4 {
		if h4, err = m.Candles(ctx, s.Symbol, "4h", 1000); err != nil {
			return
		}
	}
	if d1, err = m.Candles(ctx, s.Symbol, "1d", 300); err != nil {
		return
	}
	bars = market.ClosedOnly(bars, now)
	if d, _ := market.IntervalDuration(s.Interval); s.Bar > d {
		bars = market.Aggregate(bars, s.Bar)
	}
	return bars, market.ClosedOnly(h4, now), market.ClosedOnly(d1, now), nil
}

// Evaluate runs every strategy. Futures data is tried first; once it fails,
// spot data is used for the remaining strategies too.
func (r *Runner) Evaluate(ctx context.Context) []*Snapshot {
	now := r.now()
	useFutures := r.Futures != nil
	var out []*Snapshot
	for _, s := range r.Specs {
		snap := &Snapshot{Spec: s, At: now}
		var bars, h4, d1 []market.Candle
		var err error
		if useFutures && !s.SpotOnly {
			snap.Source = "Binance Futures (" + s.Symbol + ".P)"
			bars, h4, d1, err = fetch(ctx, r.Futures, s, now)
			if err != nil {
				r.logf("futures data unavailable (%v); using spot", err)
				useFutures = false
			}
		}
		if s.SpotOnly {
			snap.Source = "Binance Spot (" + s.Symbol + ")"
			bars, h4, d1, err = fetch(ctx, r.Spot, s, now)
		} else if !useFutures {
			snap.Source = "Binance Spot (" + s.Symbol + ") — دیتای فیوچرز در دسترس نبود"
			bars, h4, d1, err = fetch(ctx, r.Spot, s, now)
		}
		switch {
		case err != nil:
			snap.Err = err
		case len(bars) < 100:
			snap.Err = fmt.Errorf("not enough candles: %d", len(bars))
		default:
			snap.Result = Simulate(s, bars, d1, s.Build(bars, h4))
		}
		if snap.Err != nil {
			r.logf("%s: %v", s.ID, snap.Err)
		}
		out = append(out, snap)
	}
	return out
}

// staleAfter: older events are never sent (first run, or after an outage).
func staleAfter(bar time.Duration) time.Duration {
	if d := 2*bar + time.Hour; d > 7*time.Hour {
		return d
	}
	return 7 * time.Hour
}

// NotifyNew sends recent events that were not sent before, recording them in st.
func (r *Runner) NotifyNew(ctx context.Context, snaps []*Snapshot, st *store.State) (int, error) {
	if st.Notified == nil {
		st.Notified = map[string]int64{}
	}
	sent := 0
	var errs []error
	var at time.Time
	for _, snap := range snaps {
		at = snap.At
		if snap.Err != nil {
			continue
		}
		for _, e := range snap.Result.Events {
			if snap.At.Sub(e.Bar.OpenTime.Add(snap.Spec.Bar)) > staleAfter(snap.Spec.Bar) {
				continue
			}
			key := snap.Spec.ID + ":" + string(e.Kind) + ":" + strconv.FormatInt(e.Bar.OpenTime.UnixMilli(), 10)
			if _, done := st.Notified[key]; done {
				continue
			}
			if err := r.Notify.SendMessage(ctx, r.ChatID, r.FormatEvent(snap.Spec, e, snap.Source)); err != nil {
				errs = append(errs, err)
				continue
			}
			st.Notified[key] = snap.At.Unix()
			sent++
		}
	}
	for k, ts := range st.Notified {
		if at.Unix()-ts > 60*24*3600 {
			delete(st.Notified, k)
		}
	}
	return sent, errors.Join(errs...)
}

// --- formatting ------------------------------------------------------------

// Price formats a price with thousands separators and the given decimals.
func Price(v float64, decimals int) string {
	if math.IsNaN(v) {
		return "—"
	}
	s := strconv.FormatFloat(math.Abs(v), 'f', decimals, 64)
	intPart, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, ch := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	out := b.String()
	if hasFrac {
		out += "." + frac
	}
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

func (r *Runner) barClose(s *Spec, b market.Candle) string {
	return b.OpenTime.Add(s.Bar).In(r.loc()).Format("2006-01-02 15:04")
}

func tfName(d time.Duration) string { return strconv.Itoa(int(d.Hours())) + "h" }

var ordinals = []string{"۱", "۲", "۳"}

// FormatEvent renders an event as a Telegram message.
func (r *Runner) FormatEvent(s *Spec, e Event, source string) string {
	P := func(v float64) string { return Price(v, s.Decimals) }
	head := fmt.Sprintf("%s | %s", s.Symbol, tfName(s.Bar))
	switch e.Kind {
	case Signal:
		var b strings.Builder
		fmt.Fprintf(&b, "📣 سیگنال %s — %s\nاستراتژی: %s\n\n", dirName(e.Long), head, s.Name)
		if s.LimitValid > 0 {
			fmt.Fprintf(&b, "ورود: %s (سفارش لیمیت روی سطح شکسته‌شده، اعتبار %d کندل)\n", P(e.Entry), s.LimitValid)
		} else {
			fmt.Fprintf(&b, "ورود: ~%s (مارکت، باز شدن کندل بعد)\n", P(e.Entry))
		}
		fmt.Fprintf(&b, "حد ضرر: %s  (فاصله %s)\n", P(e.Stop), pct(e.Entry, e.Stop))
		for k, tp := range e.Targets {
			label := "تارگت"
			if len(e.Targets) > 1 {
				label += " " + ordinals[k]
			}
			fmt.Fprintf(&b, "%s: %s  (%gR — بستن %g%%)\n", label, P(tp), s.Targets[k].R, s.Targets[k].Pct)
		}
		if s.UseBE {
			b.WriteString("بعد از تارگت ۱، حد ضرر به نقطه‌ی ورود منتقل می‌شود.\n")
		}
		if s.MaxBars > 0 {
			fmt.Fprintf(&b, "خروج زمانی: اگر تا %d کندل به حد ضرر یا تارگت نرسید، بسته می‌شود.\n", s.MaxBars)
		}
		b.WriteString("\n")
		if e.Note != "" {
			b.WriteString(e.Note + "\n")
		}
		strength := "معمولی"
		if e.Strong {
			strength = "بالا (روزانه هم‌جهت با EMA20)"
		}
		fmt.Fprintf(&b, "قدرت سیگنال: %s\nریسک پیشنهادی: %g%% سرمایه\nبسته شدن کندل: %s\nداده: %s\n\n",
			strength, s.Risk.RiskPct, r.barClose(s, e.Bar), source)
		if s.LimitValid > 0 {
			fmt.Fprintf(&b, "⚠️ ابطال: اگر تا %d کندل پر نشد لغو می‌شود؛ رسیدن قیمت به حد ضرر ایده را باطل می‌کند.\n", s.LimitValid)
		} else if e.Long {
			b.WriteString("⚠️ ابطال: اگر کندل بعدی زیر حد ضرر باز شد، وارد نشوید.\n")
		} else {
			b.WriteString("⚠️ ابطال: اگر کندل بعدی بالای حد ضرر باز شد، وارد نشوید.\n")
		}
		b.WriteString("⚠️ " + s.Warning)
		return b.String()

	case Filled:
		return fmt.Sprintf("📥 سفارش لیمیت پر شد — %s %s\nورود: %s | حد ضرر: %s | تارگت: %s\nبسته شدن کندل: %s",
			head, dirName(e.Long), P(e.Fill), P(e.Stop), P(e.Targets[len(e.Targets)-1]), r.barClose(s, e.Bar))

	case Cancelled:
		return fmt.Sprintf("🚫 سفارش لیمیت %s %s در %s پر نشد و لغو شد (بعد از %d کندل).",
			dirName(e.Long), head, P(e.Entry), s.LimitValid)

	case TP1:
		return fmt.Sprintf("✅ تارگت ۱ خورد — %s %s\nحد ضرر به %s (نقطه‌ی ورود) منتقل شد.\nتارگت ۲: %s\nبسته شدن کندل: %s",
			head, dirName(e.Long), P(e.NewStop), P(e.Targets[len(e.Targets)-1]), r.barClose(s, e.Bar))

	case Exit:
		reason := map[string]string{
			"target":    "🎯 تارگت خورد",
			"stop":      "🛑 حد ضرر خورد",
			"breakeven": "⚖️ بعد از تارگت ۱ در نقطه‌ی ورود بسته شد",
			"time":      fmt.Sprintf("⏱ خروج زمانی بعد از %d کندل", s.MaxBars),
		}[e.ExitReason]
		if e.ExitReason == "target" && len(e.Targets) > 1 {
			reason = "🎯 تارگت نهایی خورد"
		}
		return fmt.Sprintf("🏁 معامله‌ی %s %s بسته شد\n%s\nنتیجه: %+.2fR (بدون کارمزد)\nبسته شدن کندل: %s",
			dirName(e.Long), head, reason, e.ResultR, r.barClose(s, e.Bar))
	}
	return ""
}

// StatusText summarises every strategy, for any message sent to the bot.
func (r *Runner) StatusText(snaps []*Snapshot) string {
	var b strings.Builder
	b.WriteString("🤖 ربات سیگنال — استراتژی‌های فعال\n")
	b.WriteString("ربات چارت‌ها را خودکار بررسی می‌کند و سیگنال‌ها را می‌فرستد؛ لازم نیست کاری بکنید.\n")
	if len(snaps) == 0 {
		b.WriteString("\n⚠️ دریافت داده ناموفق بود؛ در اجرای بعدی دوباره امتحان می‌شود.")
		return b.String()
	}
	for _, snap := range snaps {
		s := snap.Spec
		P := func(v float64) string { return Price(v, s.Decimals) }
		fmt.Fprintf(&b, "\n━━━━━━━━━━\n%s | %s — %s\n", s.Symbol, tfName(s.Bar), s.Name)
		if snap.Err != nil {
			b.WriteString("⚠️ دریافت داده ناموفق بود.\n")
			continue
		}
		bars := snap.Result.Bars
		last := bars[len(bars)-1]
		fmt.Fprintf(&b, "آخرین کندل: %s (قیمت %s)\n", r.barClose(s, last), P(last.Close))
		switch pos := snap.Result.Position; {
		case pos == nil:
			b.WriteString("وضعیت: بدون معامله — منتظر سیگنال\n")
		case pos.Pending && pos.Limit:
			fmt.Fprintf(&b, "وضعیت: سفارش لیمیت %s در %s (%d کندل مانده) | SL %s\n", dirName(pos.Long), P(pos.Entry), pos.BarsLeft, P(pos.Stop))
		case pos.Pending:
			fmt.Fprintf(&b, "وضعیت: سیگنال %s — ورود در کندل بعد | SL %s\n", dirName(pos.Long), P(pos.Stop))
		default:
			tps := make([]string, len(pos.Targets))
			for k, t := range pos.Targets {
				tps[k] = P(t)
			}
			fmt.Fprintf(&b, "وضعیت: معامله‌ی باز %s | ورود %s | SL %s | TP %s\n", dirName(pos.Long), P(pos.Entry), P(pos.Stop), strings.Join(tps, " / "))
		}
		var sigs []Event
		for _, e := range snap.Result.Events {
			if e.Kind == Signal {
				sigs = append(sigs, e)
			}
		}
		if len(sigs) > 3 {
			sigs = sigs[len(sigs)-3:]
		}
		for _, e := range sigs {
			d := "LONG "
			if !e.Long {
				d = "SHORT"
			}
			fmt.Fprintf(&b, "• %s  %s  ورود %s\n", r.barClose(s, e.Bar), d, P(e.Entry))
		}
	}
	if len(snaps) > 0 && snaps[0].Source != "" {
		fmt.Fprintf(&b, "\nداده: %s", strings.SplitN(snaps[len(snaps)-1].Source, " (", 2)[0])
	}
	return b.String()
}
