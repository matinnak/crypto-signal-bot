// Package bot handles Telegram commands: managing strategies, subscriptions
// and user access.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/matinnak/crypto-signal-bot/internal/dsl"
	"github.com/matinnak/crypto-signal-bot/internal/engine"
	"github.com/matinnak/crypto-signal-bot/internal/market"
	"github.com/matinnak/crypto-signal-bot/internal/store"
	"github.com/matinnak/crypto-signal-bot/internal/telegram"
)

// MaxSubsPerUser limits how many symbol/timeframe/strategy rows a user can add.
const MaxSubsPerUser = 50

// Sender sends a message to a chat.
type Sender interface {
	SendMessage(ctx context.Context, chatID int64, text string) error
}

// Bot handles incoming messages.
type Bot struct {
	TG         Sender
	Market     market.Provider
	AdminID    int64
	OpenAccess bool // allow anyone without admin approval
	Loc        *time.Location
	Now        func() time.Time
	Log        *log.Logger
}

func (b *Bot) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	if err := b.TG.SendMessage(ctx, chatID, text); err != nil && b.Log != nil {
		b.Log.Printf("reply to %d failed: %v", chatID, err)
	}
}

// SeedDefaults adds example strategies the first time the bot runs.
func SeedDefaults(st *store.State, now time.Time) {
	if st.Seeded {
		return
	}
	st.Seeded = true
	defaults := []store.Strategy{
		{Name: "rsi_reversal", Description: "خروج RSI از اشباع فروش (خرید) و اشباع خرید (فروش)",
			Buy: "crossover(RSI(14), 30)", Sell: "crossunder(RSI(14), 70)"},
		{Name: "ema_cross", Description: "کراس EMA 9 و EMA 21",
			Buy: "crossover(EMA(9), EMA(21))", Sell: "crossunder(EMA(9), EMA(21))"},
		{Name: "macd_trend", Description: "کراس MACD فقط بالای EMA 200",
			Buy: "crossover(MACD_line, MACD_signal) and close > EMA(200)", Sell: "crossunder(MACD_line, MACD_signal)"},
		{Name: "bb_bounce", Description: "برگشت قیمت از باندهای بولینگر",
			Buy: "crossover(close, BB_lower(20, 2))", Sell: "crossunder(close, BB_upper(20, 2))"},
	}
	for i := range defaults {
		s := defaults[i]
		if _, exists := st.Strategies[s.Name]; !exists {
			s.CreatedAt = now
			st.Strategies[s.Name] = &s
		}
	}
}

// Handle processes one incoming message and updates the state.
func (b *Bot) Handle(ctx context.Context, st *store.State, m *telegram.Message) {
	if m == nil {
		return
	}
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = strings.TrimSpace(m.Caption)
	}

	u, known := st.Users[chatID]
	if !known {
		u = &store.User{ChatID: chatID, Name: m.From.DisplayName(), RequestedAt: b.now()}
		st.Users[chatID] = u
	}
	if name := m.From.DisplayName(); name != "" {
		u.Name = name
	}
	if chatID == b.AdminID || b.OpenAccess {
		u.Approved = true
	}

	if !u.Approved {
		if !known {
			b.reply(ctx, chatID, "👋 سلام! درخواست دسترسی شما برای ادمین فرستاده شد. بعد از تأیید، پیام می‌دهیم.")
			b.reply(ctx, b.AdminID, fmt.Sprintf("🔔 درخواست دسترسی جدید:\n%s\nشناسه: %d\n\nبرای تأیید: /approve %d", orDash(u.Name), chatID, chatID))
		} else {
			b.reply(ctx, chatID, "⏳ درخواست شما هنوز در انتظار تأیید ادمین است.")
		}
		return
	}

	if !strings.HasPrefix(text, "/") {
		b.reply(ctx, chatID, "دستور با / شروع می‌شود. برای راهنما /help را بفرستید.")
		return
	}

	firstLine, body, _ := strings.Cut(text, "\n")
	fields := strings.Fields(firstLine)
	cmd := strings.ToLower(fields[0])
	if i := strings.Index(cmd, "@"); i >= 0 { // "/add@MyBot" in groups
		cmd = cmd[:i]
	}
	args := fields[1:]
	isAdmin := chatID == b.AdminID

	var out string
	switch cmd {
	case "/start", "/help":
		out = helpText(isAdmin)
	case "/indicators":
		out = indicatorsText()
	case "/strategies":
		out = strategiesText(st)
	case "/add":
		out = b.cmdAdd(ctx, st, u, args)
	case "/list":
		out = listText(u)
	case "/remove":
		out = cmdRemove(u, args)
	case "/test":
		out = b.cmdTest(ctx, st, args)
	case "/new":
		out = adminOnly(isAdmin, func() string { return b.cmdNew(st, chatID, args, body) })
	case "/delstrategy":
		out = adminOnly(isAdmin, func() string { return cmdDelStrategy(st, args) })
	case "/users":
		out = adminOnly(isAdmin, func() string { return usersText(st, b.AdminID) })
	case "/approve":
		out = adminOnly(isAdmin, func() string { return b.cmdApprove(ctx, st, args, true) })
	case "/revoke":
		out = adminOnly(isAdmin, func() string { return b.cmdApprove(ctx, st, args, false) })
	default:
		out = "دستور ناشناخته. برای راهنما /help را بفرستید."
	}
	b.reply(ctx, chatID, out)
}

func adminOnly(isAdmin bool, f func() string) string {
	if !isAdmin {
		return "⛔ این دستور فقط برای ادمین است."
	}
	return f()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func helpText(isAdmin bool) string {
	var sb strings.Builder
	sb.WriteString(`🤖 ربات سیگنال

هر وقت شرط استراتژی روی یک کندل بسته‌شده برقرار شود، پیام می‌دهد.

دستورها:
/strategies — لیست استراتژی‌ها
/add BTCUSDT 4h 1 — اضافه کردن ارز (1 = شماره یا اسم استراتژی)
   چند ارز و تایم‌فریم با هم:
   /add BTCUSDT,ETHUSDT 1h,4h ema_cross
/list — ارزهای فعال شما
/remove 2 — حذف ردیف ۲ از /list (یا /remove all)
/test ema_cross BTCUSDT 4h — سیگنال‌های اخیر استراتژی روی گذشته
/indicators — اندیکاتورها و نحوه‌ی نوشتن شرط

تایم‌فریم‌ها: `)
	sb.WriteString(strings.Join(market.SupportedIntervals(), " "))
	if isAdmin {
		sb.WriteString(`

👑 دستورهای ادمین:
/new — ساخت یا ویرایش استراتژی، مثلاً:
/new rsi_ema
buy: RSI(14) < 30 and close > EMA(200)
sell: RSI(14) > 70
desc: خرید در اشباع فروش در روند صعودی

/delstrategy rsi_ema — حذف استراتژی
/users — لیست کاربران
/approve 123456 — تأیید کاربر
/revoke 123456 — لغو دسترسی`)
	}
	return sb.String()
}

func indicatorsText() string {
	var sb strings.Builder
	sb.WriteString("📐 اندیکاتورها (نمونه‌ی استفاده):\n")
	for _, h := range dsl.FunctionHelp() {
		sb.WriteString("• " + h + "\n")
	}
	sb.WriteString(`
مقادیر کندل: open high low close volume hl2 hlc3 ohlc4
کندل‌های قبلی: close[1] یعنی close کندل قبل
عملگرها: < > <= >= == != + - * / and or not ( )
اگر منبع ننویسید، close در نظر گرفته می‌شود: EMA(200) = EMA(close, 200)

نمونه‌ها:
RSI(14) < 30 and close > EMA(200)
crossover(EMA(9), EMA(21))
volume > SMA(volume, 20) * 1.5
close > highest(high, 20)[1]`)
	return sb.String()
}

func strategiesText(st *store.State) string {
	names := st.StrategyNames()
	if len(names) == 0 {
		return "هنوز استراتژی‌ای تعریف نشده."
	}
	var sb strings.Builder
	sb.WriteString("📚 استراتژی‌ها:\n")
	for i, n := range names {
		s := st.Strategies[n]
		fmt.Fprintf(&sb, "\n%d. %s", i+1, s.Name)
		if s.Description != "" {
			sb.WriteString(" — " + s.Description)
		}
		if s.Buy != "" {
			sb.WriteString("\n   🟢 buy: " + s.Buy)
		}
		if s.Sell != "" {
			sb.WriteString("\n   🔴 sell: " + s.Sell)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nبرای اضافه کردن: /add BTCUSDT 4h 1")
	return sb.String()
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (b *Bot) cmdAdd(ctx context.Context, st *store.State, u *store.User, args []string) string {
	if len(args) != 3 {
		return "فرمت درست:\n/add BTCUSDT 4h 1\nیا چند تا با هم:\n/add BTCUSDT,ETHUSDT 1h,4h ema_cross"
	}
	strat, ok := st.FindStrategy(args[2])
	if !ok {
		return fmt.Sprintf("استراتژی %q پیدا نشد. لیست: /strategies", args[2])
	}
	var intervals []string
	for _, tf := range splitList(args[1]) {
		tf = market.NormalizeInterval(tf)
		if !market.ValidInterval(tf) {
			return fmt.Sprintf("تایم‌فریم %q معتبر نیست. مجازها: %s", tf, strings.Join(market.SupportedIntervals(), " "))
		}
		intervals = append(intervals, tf)
	}

	var added, skipped, bad []string
	for _, raw := range splitList(args[0]) {
		sym := market.NormalizeSymbol(raw)
		if err := b.checkSymbol(ctx, sym); err != nil {
			bad = append(bad, fmt.Sprintf("%s (%v)", sym, err))
			continue
		}
		for _, tf := range intervals {
			sub := &store.Subscription{Symbol: sym, Interval: tf, Strategy: strat.Name}
			if hasSub(u, sub) {
				skipped = append(skipped, sym+" "+tf)
				continue
			}
			if len(u.Subs) >= MaxSubsPerUser {
				bad = append(bad, fmt.Sprintf("%s %s (سقف %d ردیف پر است)", sym, tf, MaxSubsPerUser))
				continue
			}
			u.Subs = append(u.Subs, sub)
			added = append(added, sym+" "+tf)
		}
	}

	var sb strings.Builder
	if len(added) > 0 {
		fmt.Fprintf(&sb, "✅ با استراتژی %s اضافه شد:\n%s\n", strat.Name, strings.Join(added, "\n"))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(&sb, "\nاز قبل وجود داشت:\n%s\n", strings.Join(skipped, "\n"))
	}
	if len(bad) > 0 {
		fmt.Fprintf(&sb, "\n❌ اضافه نشد:\n%s\n", strings.Join(bad, "\n"))
	}
	return strings.TrimSpace(sb.String())
}

func (b *Bot) checkSymbol(ctx context.Context, sym string) error {
	if !symbolPattern.MatchString(sym) {
		return errors.New("نماد نامعتبر")
	}
	if b.Market == nil {
		return nil
	}
	_, err := b.Market.Candles(ctx, sym, "1d", 1)
	switch {
	case errors.Is(err, market.ErrInvalidSymbol):
		return errors.New("در صرافی پیدا نشد")
	case err != nil:
		return errors.New("اتصال به صرافی ناموفق بود، دوباره امتحان کنید")
	}
	return nil
}

func hasSub(u *store.User, s *store.Subscription) bool {
	for _, x := range u.Subs {
		if x.Key() == s.Key() {
			return true
		}
	}
	return false
}

func listText(u *store.User) string {
	if len(u.Subs) == 0 {
		return "هنوز ارزی اضافه نکرده‌اید. مثال: /add BTCUSDT 4h 1"
	}
	var sb strings.Builder
	sb.WriteString("📋 ارزهای فعال شما:\n")
	for i, s := range u.Subs {
		fmt.Fprintf(&sb, "%d. %s | %s | %s\n", i+1, s.Symbol, s.Interval, s.Strategy)
	}
	sb.WriteString("\nبرای حذف: /remove شماره")
	return sb.String()
}

func cmdRemove(u *store.User, args []string) string {
	if len(args) == 0 {
		return "فرمت درست: /remove 2 (شماره از /list) یا /remove all"
	}
	if strings.EqualFold(args[0], "all") {
		n := len(u.Subs)
		u.Subs = nil
		return fmt.Sprintf("🗑 همه‌ی %d ردیف حذف شد.", n)
	}
	// Accept several numbers: "/remove 1 3" or "/remove 1,3".
	nums := map[int]bool{}
	for _, a := range args {
		for _, p := range splitList(a) {
			n, err := strconv.Atoi(p)
			if err != nil || n < 1 || n > len(u.Subs) {
				return fmt.Sprintf("شماره‌ی %q معتبر نیست. /list را ببینید.", p)
			}
			nums[n] = true
		}
	}
	var kept []*store.Subscription
	var removed []string
	for i, s := range u.Subs {
		if nums[i+1] {
			removed = append(removed, fmt.Sprintf("%s | %s | %s", s.Symbol, s.Interval, s.Strategy))
			continue
		}
		kept = append(kept, s)
	}
	u.Subs = kept
	return "🗑 حذف شد:\n" + strings.Join(removed, "\n")
}

func (b *Bot) cmdTest(ctx context.Context, st *store.State, args []string) string {
	if len(args) != 3 {
		return "فرمت درست: /test ema_cross BTCUSDT 4h"
	}
	strat, ok := st.FindStrategy(args[0])
	if !ok {
		return fmt.Sprintf("استراتژی %q پیدا نشد. لیست: /strategies", args[0])
	}
	sym := market.NormalizeSymbol(args[1])
	tf := market.NormalizeInterval(args[2])
	if !market.ValidInterval(tf) {
		return fmt.Sprintf("تایم‌فریم %q معتبر نیست.", tf)
	}
	sigs, bars, err := engine.Backtest(ctx, b.Market, strat, sym, tf, b.now(), 10)
	if errors.Is(err, market.ErrInvalidSymbol) {
		return fmt.Sprintf("ارز %s در صرافی پیدا نشد.", sym)
	}
	if err != nil {
		return "خطا در اجرای تست: " + err.Error()
	}
	if len(sigs) == 0 {
		return fmt.Sprintf("در %d کندل اخیر %s %s هیچ سیگنالی از %s نبود.", bars, sym, tf, strat.Name)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧪 %d سیگنال آخر %s روی %s %s (از %d کندل):\n\n", len(sigs), strat.Name, sym, tf, bars)
	for _, s := range sigs {
		icon := "🟢 BUY "
		if s.Side == engine.Sell {
			icon = "🔴 SELL"
		}
		fmt.Fprintf(&sb, "%s  %s  قیمت %s\n", icon, engine.CloseTime(s.Candle).In(b.loc()).Format("2006-01-02 15:04"), engine.FormatPrice(s.Candle.Close))
	}
	sb.WriteString("\nزمان‌ها = بسته شدن کندل. با چارت TradingView مقایسه کنید.")
	return sb.String()
}

func (b *Bot) loc() *time.Location {
	if b.Loc == nil {
		return time.UTC
	}
	return b.Loc
}

var (
	strategyName  = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	symbolPattern = regexp.MustCompile(`^[A-Z0-9]{3,20}$`)
)

// ParseStrategy reads the body of a /new command:
//
//	buy: <condition>
//	sell: <condition>
//	desc: <text>
//
// Lines without a recognised key continue the previous key's value.
func ParseStrategy(name, body string) (*store.Strategy, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strategyName.MatchString(name) {
		return nil, errors.New("اسم استراتژی باید با حرف انگلیسی شروع شود و فقط حروف کوچک انگلیسی، عدد و _ داشته باشد (۲ تا ۳۲ کاراکتر)، مثل rsi_ema")
	}
	s := &store.Strategy{Name: name}
	var cur *string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, found := strings.Cut(line, ":")
		var target *string
		if found {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "buy", "خرید":
				target = &s.Buy
			case "sell", "فروش":
				target = &s.Sell
			case "desc", "description", "توضیح":
				target = &s.Description
			}
		}
		if target != nil {
			*target = strings.TrimSpace(val)
			cur = target
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("خط %q مشخص نیست؛ هر خط باید با buy: یا sell: یا desc: شروع شود", line)
		}
		*cur = strings.TrimSpace(*cur + " " + line)
	}
	if _, err := engine.CompileStrategy(s); err != nil {
		return nil, err
	}
	return s, nil
}

func (b *Bot) cmdNew(st *store.State, chatID int64, args []string, body string) string {
	usage := "فرمت درست:\n/new rsi_ema\nbuy: RSI(14) < 30 and close > EMA(200)\nsell: RSI(14) > 70\ndesc: توضیح دلخواه"
	if len(args) == 0 {
		return usage
	}
	// Allow "/new name buy: ..." on one line too.
	name := args[0]
	if len(args) > 1 {
		body = strings.Join(args[1:], " ") + "\n" + body
	}
	if strings.TrimSpace(body) == "" {
		return usage
	}
	s, err := ParseStrategy(name, body)
	if err != nil {
		return "❌ " + err.Error()
	}
	s.CreatedBy = chatID
	s.CreatedAt = b.now()
	_, existed := st.Strategies[s.Name]
	st.Strategies[s.Name] = s
	verb := "ساخته شد"
	if existed {
		verb = "به‌روزرسانی شد"
	}
	return fmt.Sprintf("✅ استراتژی %s %s.\n\nقبل از استفاده تستش کنید:\n/test %s BTCUSDT 4h", s.Name, verb, s.Name)
}

func cmdDelStrategy(st *store.State, args []string) string {
	if len(args) != 1 {
		return "فرمت درست: /delstrategy rsi_ema"
	}
	s, ok := st.FindStrategy(args[0])
	if !ok {
		return "استراتژی پیدا نشد."
	}
	if n := st.StrategyUsers(s.Name); n > 0 {
		return fmt.Sprintf("❌ %d ردیف از این استراتژی استفاده می‌کنند. اول آن‌ها باید با /remove حذف شوند.", n)
	}
	delete(st.Strategies, s.Name)
	return "🗑 استراتژی " + s.Name + " حذف شد."
}

func usersText(st *store.State, adminID int64) string {
	if len(st.Users) == 0 {
		return "کاربری وجود ندارد."
	}
	var sb strings.Builder
	sb.WriteString("👥 کاربران:\n")
	ids := make([]int64, 0, len(st.Users))
	for id := range st.Users {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		u := st.Users[id]
		status := "⏳ در انتظار"
		if u.Approved {
			status = "✅"
		}
		if id == adminID {
			status = "👑"
		}
		fmt.Fprintf(&sb, "%s %s — %d — %d ردیف\n", status, orDash(u.Name), id, len(u.Subs))
	}
	return sb.String()
}

func (b *Bot) cmdApprove(ctx context.Context, st *store.State, args []string, approve bool) string {
	if len(args) != 1 {
		return "فرمت درست: /approve 123456"
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return "شناسه باید عدد باشد."
	}
	if id == b.AdminID {
		return "دسترسی ادمین قابل تغییر نیست."
	}
	u, ok := st.Users[id]
	if !ok {
		if !approve {
			return "کاربر پیدا نشد."
		}
		u = &store.User{ChatID: id, RequestedAt: b.now()}
		st.Users[id] = u
	}
	u.Approved = approve
	if approve {
		b.reply(ctx, id, "✅ دسترسی شما تأیید شد. برای شروع /help را بفرستید.")
		return fmt.Sprintf("✅ کاربر %d تأیید شد.", id)
	}
	return fmt.Sprintf("⛔ دسترسی کاربر %d لغو شد. (ردیف‌هایش نگه داشته می‌شود ولی سیگنالی نمی‌گیرد)", id)
}
