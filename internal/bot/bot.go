package bot

import (
	"context"
	"errors"
	"html"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"fogwatch/internal/ai"
	"fogwatch/internal/i18n"
	"fogwatch/internal/store"
	"fogwatch/internal/telegram"
)

var (
	codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{3,29}$`)
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

const allChunk = 40 // lines per /all message (text limit 4096)

type Bot struct {
	tg      *telegram.Client
	st      *store.Store
	ai      *ai.Client
	admins  map[int64]bool
	loc     *time.Location
	version string
	fetchMu sync.Mutex

	// Scheduled-search health; touched only by ScheduledFetch, which the
	// cron runs one at a time (SkipIfStillRunning).
	fetchFails   int
	fetchAlerted bool
}

const (
	knownCodesInPrompt = 200 // most recent codes listed to the LLM as already known
	fetchAlertAfter    = 2   // consecutive scheduled failures before admins hear about it
)

func New(tg *telegram.Client, st *store.Store, llm *ai.Client, admins map[int64]bool, loc *time.Location, version string) *Bot {
	return &Bot{tg: tg, st: st, ai: llm, admins: admins, loc: loc, version: version}
}

// menuCodes are the client language codes menus are set for, "" being the
// fallback for all others; menuLang maps each to a menu language.
var menuCodes = []string{"", "ru", "uk", "be", "kk"}

func menuLang(code string) string {
	if code == "" {
		return "en"
	}
	return i18n.Detect(code)
}

// SetupMenus sets the command menus: default for everyone, extended for
// admins. An admin's chat menu fails with "chat not found" until they've
// pressed /start once; restart after that.
func (b *Bot) SetupMenus(ctx context.Context) {
	for _, code := range menuCodes {
		if err := b.tg.SetCommands(ctx, i18n.Commands[menuLang(code)], code, 0); err != nil {
			log.Printf("setMyCommands %q: %v", code, err)
		}
	}
	for id := range b.admins {
		b.chatMenu(ctx, id, b.st.ChosenLang(id))
	}
}

// chatMenu pins a user's menu to the language chosen in /lang; chosen ""
// goes back to the client language. Telegram picks the menu by the client
// language, but a chat-scoped one wins, with language_code before without:
// so users get one menu without it, and admins (whose chat menus carry admin
// commands) get every language variant.
func (b *Bot) chatMenu(ctx context.Context, id int64, chosen string) {
	if !b.admins[id] {
		var err error
		if chosen == "" {
			err = b.tg.DeleteCommands(ctx, "", id)
		} else {
			err = b.tg.SetCommands(ctx, i18n.Commands[chosen], "", id)
		}
		if err != nil {
			log.Printf("chat menu %d: %v", id, err)
		}
		return
	}
	for _, code := range menuCodes {
		lang := chosen
		if lang == "" {
			lang = menuLang(code)
		}
		all := append(append([]telegram.Cmd{}, i18n.Commands[lang]...), i18n.AdminCommands[lang]...)
		if err := b.tg.SetCommands(ctx, all, code, id); err != nil {
			log.Printf("setMyCommands admin %d %q: %v", id, code, err)
		}
	}
}

// Run long-polls Telegram and handles updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	offset := 0
	for ctx.Err() == nil {
		ups, err := b.tg.Updates(ctx, offset)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("getUpdates: %v", err)
				time.Sleep(3 * time.Second)
			}
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			b.Handle(ctx, u)
		}
	}
}

func (b *Bot) today() string { return time.Now().In(b.loc).Format(time.DateOnly) }

func normCode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func validDate(s string) bool {
	if !dateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// ---------- rendering ----------

func codeLine(lang string, c store.Code) string {
	s := "<code>" + html.EscapeString(c.Code) + "</code>"
	if r := c.Reward(lang); r != "" {
		s += " — " + html.EscapeString(r)
	}
	if c.Expires != "" {
		s += " · " + i18n.T(lang, "until", c.Expires)
	}
	return s
}

// codeMsg is one code per message: delete the message once redeemed.
func codeMsg(lang, icon string, c store.Code) (string, *telegram.Keyboard) {
	return icon + " " + codeLine(lang, c), &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{{
		{Text: "📋 " + i18n.T(lang, "copy"), CopyText: &telegram.CopyText{Text: c.Code}},
	}}}
}

func (b *Bot) sendCodes(ctx context.Context, chat int64, lang, icon string, codes []store.Code) error {
	for _, c := range codes {
		text, kb := codeMsg(lang, icon, c)
		if err := b.tg.Send(ctx, chat, text, kb); err != nil {
			return err
		}
	}
	return nil
}

// ---------- updates ----------

func (b *Bot) Handle(ctx context.Context, u telegram.Update) {
	switch {
	case u.Message != nil:
		b.onMessage(ctx, u.Message)
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string, kb *telegram.Keyboard) {
	if err := b.tg.Send(ctx, chatID, text, kb); err != nil {
		log.Printf("send %d: %v", chatID, err)
	}
}

// dbFail logs a store error and tells the user something went wrong.
func (b *Bot) dbFail(ctx context.Context, chat int64, lang string, err error) {
	log.Printf("db: %v", err)
	b.reply(ctx, chat, i18n.T(lang, "db_err"), nil)
}

func (b *Bot) onMessage(ctx context.Context, m *telegram.Message) {
	if m.From == nil || m.Chat.Type != "private" || m.Text == "" {
		return
	}
	uid, chat := m.From.ID, m.Chat.ID
	lang, err := b.st.Touch(uid, i18n.Detect(m.From.LanguageCode))
	if err != nil {
		b.dbFail(ctx, chat, b.st.Lang(uid), err)
		return
	}
	isAdmin := b.admins[uid]

	f := strings.Fields(m.Text)
	cmd, args := strings.ToLower(f[0]), f[1:]
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}

	switch cmd {
	case "/start":
		b.reply(ctx, chat, b.help(lang, isAdmin), nil)
		b.cmdCodes(ctx, chat, lang)
	case "/help":
		b.reply(ctx, chat, b.help(lang, isAdmin), nil)
	case "/codes":
		b.cmdCodes(ctx, chat, lang)
	case "/lang":
		b.reply(ctx, chat, i18n.T(lang, "pick_lang"), &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{
			{{Text: "Русский", CallbackData: "l:ru"}, {Text: "English", CallbackData: "l:en"}},
			{{Text: i18n.T(lang, "lang_auto_btn"), CallbackData: "l:auto"}},
		}})
	case "/add", "/del", "/fetch", "/all", "/pending":
		if !isAdmin {
			b.reply(ctx, chat, i18n.T(lang, "no_access"), nil)
			return
		}
		switch cmd {
		case "/all":
			b.cmdAll(ctx, chat, lang)
		case "/pending":
			b.cmdPending(ctx, chat, lang)
		case "/add":
			b.adminAdd(ctx, chat, lang, args)
		case "/del":
			b.adminDel(ctx, chat, lang, args)
		case "/fetch":
			go b.adminFetch(ctx, chat, lang)
		}
	default:
		b.reply(ctx, chat, b.help(lang, isAdmin), nil)
	}
}

func (b *Bot) help(lang string, isAdmin bool) string {
	t := i18n.T(lang, "help")
	if isAdmin {
		t += i18n.T(lang, "admin_help")
	}
	return t + i18n.T(lang, "version", html.EscapeString(b.version))
}

func (b *Bot) cmdCodes(ctx context.Context, chat int64, lang string) {
	codes, err := b.st.Active(b.today())
	switch {
	case err != nil:
		b.dbFail(ctx, chat, lang, err)
	case len(codes) == 0:
		b.reply(ctx, chat, i18n.T(lang, "no_codes"), nil)
	default:
		if err := b.sendCodes(ctx, chat, lang, "🎁", codes); err != nil {
			log.Printf("send codes %d: %v", chat, err)
		}
	}
}

// cmdAll is a compact text list of everything in the database.
func (b *Bot) cmdAll(ctx context.Context, chat int64, lang string) {
	codes, err := b.st.All()
	if err != nil {
		b.dbFail(ctx, chat, lang, err)
		return
	}
	if len(codes) == 0 {
		b.reply(ctx, chat, i18n.T(lang, "no_codes"), nil)
		return
	}
	today := b.today()
	for i := 0; i < len(codes); i += allChunk {
		var sb strings.Builder
		sb.WriteString(i18n.T(lang, "all"))
		for _, c := range codes[i:min(i+allChunk, len(codes))] {
			icon := "🎁"
			switch {
			case c.Status == store.StatusPending:
				icon = "⏳"
			case !c.Active(today):
				icon = "⌛"
			}
			sb.WriteString("\n" + icon + " " + codeLine(lang, c))
		}
		b.reply(ctx, chat, sb.String(), nil)
	}
}

func (b *Bot) onCallback(ctx context.Context, q *telegram.CallbackQuery) {
	kind, val, _ := strings.Cut(q.Data, ":")
	switch kind {
	case "l":
		_ = b.tg.Answer(ctx, q.ID, "")
		choice := val
		switch val {
		case "ru", "en":
		case "auto":
			choice = ""
		default:
			return
		}
		if err := b.st.SetLang(q.From.ID, choice); err != nil {
			log.Printf("db: %v", err)
			return
		}
		b.chatMenu(ctx, q.From.ID, choice)
		lang, err := b.st.Touch(q.From.ID, i18n.Detect(q.From.LanguageCode))
		if err != nil {
			log.Printf("db: %v", err)
			return
		}
		key := "lang_set"
		if choice == "" {
			key = "lang_auto_set"
		}
		if q.Message != nil {
			_ = b.tg.EditText(ctx, q.Message.Chat.ID, q.Message.MessageID, i18n.T(lang, key), nil)
		}
	case "ok", "no":
		b.onModerate(ctx, q, kind == "ok", val)
	default:
		_ = b.tg.Answer(ctx, q.ID, "")
	}
}

// ---------- moderation ----------

// modCard is what admins see for a pending code.
func modCard(lang string, c store.Code) (string, *telegram.Keyboard) {
	text := i18n.T(lang, "mod_title") + "\n" + codeLine(lang, c)
	if c.URL != "" {
		text += "\n" + i18n.T(lang, "source") + ": " + html.EscapeString(c.URL)
	}
	return text, &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{
		{{Text: "📋 " + i18n.T(lang, "copy"), CopyText: &telegram.CopyText{Text: c.Code}}},
		{
			{Text: i18n.T(lang, "btn_approve"), CallbackData: "ok:" + c.Code},
			{Text: i18n.T(lang, "btn_reject"), CallbackData: "no:" + c.Code},
		},
	}}
}

func (b *Bot) sendModCards(ctx context.Context, chat int64, lang string, codes []store.Code) {
	for _, c := range codes {
		text, kb := modCard(lang, c)
		if err := b.tg.Send(ctx, chat, text, kb); err != nil {
			log.Printf("moderation %d: %v", chat, err)
			return
		}
	}
}

// askAdmins sends new pending codes to every admin.
func (b *Bot) askAdmins(ctx context.Context, codes []store.Code) {
	for id := range b.admins {
		b.sendModCards(ctx, id, b.st.Lang(id), codes)
	}
}

func (b *Bot) cmdPending(ctx context.Context, chat int64, lang string) {
	codes, err := b.st.Pending()
	switch {
	case err != nil:
		b.dbFail(ctx, chat, lang, err)
	case len(codes) == 0:
		b.reply(ctx, chat, i18n.T(lang, "no_pending"), nil)
	default:
		b.sendModCards(ctx, chat, lang, codes)
	}
}

func (b *Bot) onModerate(ctx context.Context, q *telegram.CallbackQuery, approve bool, code string) {
	lang := b.st.Lang(q.From.ID)
	if !b.admins[q.From.ID] {
		_ = b.tg.Answer(ctx, q.ID, i18n.T(lang, "no_access"))
		return
	}
	to, verdict := store.StatusRemoved, "rejected"
	if approve {
		to, verdict = store.StatusPublished, "approved"
	}
	c, ok, err := b.st.Moderate(code, to)
	switch {
	case err != nil:
		log.Printf("db: %v", err)
		_ = b.tg.Answer(ctx, q.ID, i18n.T(lang, "db_err"))
		return
	case !ok:
		// Stale card (second click, or another admin was faster): show the
		// actual outcome instead.
		_ = b.tg.Answer(ctx, q.ID, i18n.T(lang, "mod_done_already"))
		if c.Code != "" {
			outcome := "mod_removed"
			if c.Status == store.StatusPublished {
				outcome = "mod_published"
			}
			b.closeCard(ctx, q.Message, lang, c, outcome)
		}
		return
	}
	_ = b.tg.Answer(ctx, q.ID, i18n.T(lang, verdict))
	b.closeCard(ctx, q.Message, lang, c, verdict)
	if approve {
		// Admins already have the code in their card.
		go b.broadcast(context.WithoutCancel(ctx), []store.Code{c}, true)
	}
}

// closeCard appends the outcome to a moderation card and leaves only the
// copy button: for admins the card is the code's message.
func (b *Bot) closeCard(ctx context.Context, m *telegram.Message, lang string, c store.Code, outcome string) {
	if m == nil {
		return
	}
	text, _ := modCard(lang, c)
	kb := &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{{{Text: "📋 " + i18n.T(lang, "copy"), CopyText: &telegram.CopyText{Text: c.Code}}}}}
	if err := b.tg.EditText(ctx, m.Chat.ID, m.MessageID, text+"\n\n"+i18n.T(lang, outcome), kb); err != nil {
		log.Printf("edit: %v", err)
	}
}

// ---------- admin ----------

func (b *Bot) adminAdd(ctx context.Context, chat int64, lang string, args []string) {
	if len(args) == 0 || !codeRe.MatchString(normCode(args[0])) {
		b.reply(ctx, chat, i18n.T(lang, "add_usage"), nil)
		return
	}
	c := store.Code{Code: normCode(args[0]), Source: "admin"}
	rest := args[1:]
	if len(rest) > 0 && validDate(rest[0]) {
		c.Expires, rest = rest[0], rest[1:]
	}
	// "ru text | en text"; without "|" the same text is used for both.
	ru, en, ok := strings.Cut(strings.Join(rest, " "), "|")
	c.RewardRU, c.RewardEN = strings.TrimSpace(ru), strings.TrimSpace(en)
	if !ok {
		c.RewardEN = c.RewardRU
	}
	isNew, err := b.st.Upsert(c)
	switch {
	case err != nil:
		b.dbFail(ctx, chat, lang, err)
	case isNew:
		b.reply(ctx, chat, i18n.T(lang, "added", html.EscapeString(c.Code)), nil)
		go b.broadcast(context.WithoutCancel(ctx), []store.Code{c}, false)
	default:
		b.reply(ctx, chat, i18n.T(lang, "updated", html.EscapeString(c.Code)), nil)
	}
}

func (b *Bot) adminDel(ctx context.Context, chat int64, lang string, args []string) {
	if len(args) != 1 {
		b.reply(ctx, chat, i18n.T(lang, "del_usage"), nil)
		return
	}
	code := normCode(args[0])
	ok, err := b.st.Remove(code)
	switch {
	case err != nil:
		b.dbFail(ctx, chat, lang, err)
	case ok:
		b.reply(ctx, chat, i18n.T(lang, "deleted", html.EscapeString(code)), nil)
	default:
		b.reply(ctx, chat, i18n.T(lang, "not_found"), nil)
	}
}

func (b *Bot) adminFetch(ctx context.Context, chat int64, lang string) {
	b.reply(ctx, chat, i18n.T(lang, "fetching"), nil)
	found, added, err := b.Fetch(ctx)
	switch {
	case errors.Is(err, errBusy):
		b.reply(ctx, chat, i18n.T(lang, "fetch_busy"), nil)
	case err != nil:
		log.Printf("fetch (manual): %v", err)
		b.reply(ctx, chat, i18n.T(lang, "fetch_err"), nil)
	default:
		b.reply(ctx, chat, i18n.T(lang, "fetch_done", found, added), nil)
	}
}

// ---------- jobs ----------

var errBusy = errors.New("fetch already running")

// Fetch asks OpenAI for codes, stores valid ones as pending and sends
// new ones to admins for moderation.
func (b *Bot) Fetch(ctx context.Context) (found, added int, err error) {
	if !b.fetchMu.TryLock() {
		return 0, 0, errBusy
	}
	defer b.fetchMu.Unlock()

	today := b.today()
	known, err := b.st.KnownCodes(knownCodesInPrompt)
	if err != nil {
		return 0, 0, err
	}
	res, err := b.ai.Fetch(ctx, today, known)
	if err != nil {
		return 0, 0, err
	}
	var fresh []store.Code // new pending codes
	for _, f := range res {
		c := store.Code{
			Code:     normCode(f.Code),
			RewardEN: trunc(f.RewardEN, 80),
			RewardRU: trunc(f.RewardRU, 80),
			Source:   "ai",
			URL:      f.SourceURL,
		}
		if !codeRe.MatchString(c.Code) || !strings.HasPrefix(c.URL, "http") {
			continue // malformed or unsourced: likely hallucinated
		}
		if validDate(f.Expires) {
			if f.Expires < today {
				continue
			}
			c.Expires = f.Expires
		}
		found++
		isNew, err := b.st.Upsert(c)
		if err != nil {
			return found, len(fresh), err
		}
		if isNew {
			c.Status = store.StatusPending
			fresh = append(fresh, c)
		}
	}
	log.Printf("fetch: found %d, new %d", found, len(fresh))
	if len(fresh) > 0 {
		b.askAdmins(ctx, fresh)
	}
	return found, len(fresh), nil
}

// ScheduledFetch is the cron job: Fetch plus admin alerts. Admins are told
// once after fetchAlertAfter consecutive failures and once when it recovers,
// so a long outage doesn't spam them every 3 hours.
func (b *Bot) ScheduledFetch(ctx context.Context) {
	_, _, err := b.Fetch(ctx)
	switch {
	case errors.Is(err, errBusy):
		return // an admin /fetch is running; not a failure
	case err != nil:
		b.fetchFails++
		log.Printf("fetch (failure %d): %v", b.fetchFails, err)
		if b.fetchFails >= fetchAlertAfter && !b.fetchAlerted {
			b.fetchAlerted = true
			b.tellAdmins(ctx, func(lang string) string { return i18n.T(lang, "fetch_alert", b.fetchFails) })
		}
	default:
		if b.fetchAlerted {
			b.tellAdmins(ctx, func(lang string) string { return i18n.T(lang, "fetch_recovered") })
		}
		b.fetchFails, b.fetchAlerted = 0, false
	}
}

func (b *Bot) tellAdmins(ctx context.Context, text func(lang string) string) {
	for id := range b.admins {
		b.reply(ctx, id, text(b.st.Lang(id)), nil)
	}
}

// broadcast sends each new code as its own message to every user, once.
// broadcast sends codes to every user; skipAdmins leaves out admins who
// already got them as moderation cards.
func (b *Bot) broadcast(ctx context.Context, codes []store.Code, skipAdmins bool) {
	users, err := b.st.Users()
	if err != nil {
		log.Printf("db: %v", err)
		return
	}
	for id, lang := range users {
		if skipAdmins && b.admins[id] {
			continue
		}
		err := b.sendCodes(ctx, id, lang, "🆕", codes)
		switch {
		case telegram.IsBlocked(err):
			if err := b.st.DeleteUser(id); err != nil {
				log.Printf("db: %v", err)
			}
		case err != nil:
			log.Printf("broadcast %d: %v", id, err)
		}
		time.Sleep(50 * time.Millisecond) // stay under ~30 msg/s across chats
	}
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
