package bot

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"regexp"
	"slices"
	"strconv"
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

const listPage = 10 // codes per /codes page; copy buttons go two per row

type Bot struct {
	tg      *telegram.Client
	st      *store.Store
	ai      *ai.Client
	admins  map[int64]bool
	loc     *time.Location
	version string
	fetchMu sync.Mutex

	// FreshFor: codes without an expiry date added within this long are
	// "fresh": sent on /start and listed first in /codes. 0 = all are fresh.
	FreshFor time.Duration

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
	return &Bot{tg: tg, st: st, ai: llm, admins: admins, loc: loc, version: version, FreshFor: 7 * 24 * time.Hour}
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
		b.sendFresh(ctx, chat, lang)
	case "/help":
		b.reply(ctx, chat, b.help(lang, isAdmin), nil)
	case "/codes":
		text, kb, err := b.listPage(lang, "a", 0, isAdmin)
		if err != nil {
			b.dbFail(ctx, chat, lang, err)
			return
		}
		b.reply(ctx, chat, text, kb)
	case "/lang":
		b.reply(ctx, chat, i18n.T(lang, "pick_lang"), &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{
			{{Text: "Русский", CallbackData: "l:ru"}, {Text: "English", CallbackData: "l:en"}},
			{{Text: i18n.T(lang, "lang_auto_btn"), CallbackData: "l:auto"}},
		}})
	case "/add", "/del", "/fetch", "/pending":
		if !isAdmin {
			b.reply(ctx, chat, i18n.T(lang, "no_access"), nil)
			return
		}
		switch cmd {
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

// sendFresh sends active codes one per message, leaving out the ones without
// an expiry date added more than FreshFor ago: those would otherwise pile up
// on every /start. /codes lists them all.
func (b *Bot) sendFresh(ctx context.Context, chat int64, lang string) {
	codes, err := b.st.Active(b.today())
	if err != nil {
		b.dbFail(ctx, chat, lang, err)
		return
	}
	var fresh []store.Code
	for _, c := range codes {
		if b.fresh(c) {
			fresh = append(fresh, c)
		}
	}
	stale := len(codes) - len(fresh)
	switch {
	case len(codes) == 0:
		b.reply(ctx, chat, i18n.T(lang, "no_codes"), nil)
		return
	case len(fresh) == 0:
		b.reply(ctx, chat, i18n.T(lang, "no_fresh", b.freshDays(), stale), nil)
		return
	}
	if err := b.sendCodes(ctx, chat, lang, "🎁", fresh); err != nil {
		log.Printf("send codes %d: %v", chat, err)
		return
	}
	if stale > 0 {
		b.reply(ctx, chat, i18n.T(lang, "more_codes", b.freshDays(), stale), nil)
	}
}

// fresh: has an expiry date or was added within FreshFor.
func (b *Bot) fresh(c store.Code) bool {
	return b.FreshFor <= 0 || c.Expires != "" || c.CreatedAt.After(time.Now().Add(-b.FreshFor))
}

func (b *Bot) freshDays() int { return max(1, int(b.FreshFor.Round(24*time.Hour)/(24*time.Hour))) }

// Groups of the /codes list, in order. Users see only the active ones.
const (
	grpFresh    = iota // with an expiry date (soonest first) or fresh
	grpPending         // admins only, as are the rest but grpStale
	grpRejected        //
	grpStale           // active, no expiry date, not fresh
	grpExpired         //
	grpRemoved         //
)

var grpIcons = [...]string{"🎁", "⏳", "🚫", "🎁", "⌛", "🗑"}

func (b *Bot) group(c store.Code, today string) int {
	switch {
	case c.Status == store.StatusPending:
		return grpPending
	case c.Status == store.StatusRejected:
		return grpRejected
	case c.Status == store.StatusRemoved:
		return grpRemoved
	case !c.Active(today):
		return grpExpired
	case !b.fresh(c):
		return grpStale
	}
	return grpFresh
}

// listCodes is what /codes lists, in order: fresh codes (expiring soonest,
// then the ones without a date, newest first), then for admins pending and
// rejected ones, then older codes without a date, then for admins expired
// and removed ones. Within a group newest first.
func (b *Bot) listCodes(admin bool) ([]store.Code, error) {
	today := b.today()
	var codes []store.Code
	var err error
	if admin {
		codes, err = b.st.All()
	} else {
		codes, err = b.st.Active(today)
		slices.Reverse(codes)
	}
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(codes, func(x, y store.Code) int {
		gx, gy := b.group(x, today), b.group(y, today)
		if gx != gy {
			return gx - gy
		}
		if gx == grpFresh && x.Expires != y.Expires {
			switch {
			case x.Expires == "":
				return 1
			case y.Expires == "":
				return -1
			}
			return strings.Compare(x.Expires, y.Expires)
		}
		return 0 // keep newest first
	})
	return codes, nil
}

// listFilters are the admin filter buttons of /codes: key, label, groups.
// "a" (everything) is the default.
var listFilters = []struct {
	key, label string
	groups     []int
}{
	{"a", "all_btn", nil},
	{"g", "🎁", []int{grpFresh, grpStale}},
	{"p", "⏳", []int{grpPending}},
	{"r", "🚫", []int{grpRejected}},
	{"e", "⌛", []int{grpExpired}},
	{"d", "🗑", []int{grpRemoved}},
}

// listPage renders page (0-based, clamped) of listCodes: one message edited
// in place by the buttons. callback_data is "p:<filter><page>"; "p:-" is the
// page counter. Admins get a filter row; for users filter is ignored.
func (b *Bot) listPage(lang, filter string, page int, admin bool) (string, *telegram.Keyboard, error) {
	all, err := b.listCodes(admin)
	if err != nil || len(all) == 0 {
		return i18n.T(lang, "no_codes"), nil, err
	}
	today := b.today()
	kb := &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{}}
	codes := all
	if admin {
		var row []telegram.Button
		for _, f := range listFilters {
			var in []store.Code
			for _, c := range all {
				if f.groups == nil || slices.Contains(f.groups, b.group(c, today)) {
					in = append(in, c)
				}
			}
			if f.key == filter {
				codes = in
			} else if len(in) == 0 && f.groups != nil {
				continue
			}
			label := f.label
			if f.groups == nil {
				label = i18n.T(lang, label)
			}
			label += " " + strconv.Itoa(len(in))
			if f.key == filter {
				label = "• " + label
			}
			row = append(row, telegram.Button{Text: label, CallbackData: "p:" + f.key + "0"})
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, row[:min(3, len(row))])
		if len(row) > 3 {
			kb.InlineKeyboard = append(kb.InlineKeyboard, row[3:])
		}
	} else {
		filter = "a"
	}

	pages := max(1, (len(codes)+listPage-1)/listPage)
	page = max(0, min(page, pages-1))
	codes = codes[page*listPage : min((page+1)*listPage, len(codes))]

	header := "list"
	if admin {
		header = "all"
	}
	var sb strings.Builder
	sb.WriteString(i18n.T(lang, header))
	if len(codes) == 0 {
		sb.WriteString("\n" + i18n.T(lang, "list_empty"))
	}
	for i, c := range codes {
		sb.WriteString("\n" + grpIcons[b.group(c, today)] + " " + codeLine(lang, c))
		btn := telegram.Button{Text: "📋 " + c.Code, CopyText: &telegram.CopyText{Text: c.Code}}
		if i%2 == 0 {
			kb.InlineKeyboard = append(kb.InlineKeyboard, []telegram.Button{btn})
		} else {
			last := len(kb.InlineKeyboard) - 1
			kb.InlineKeyboard[last] = append(kb.InlineKeyboard[last], btn)
		}
	}
	if pages > 1 {
		nav := []telegram.Button{}
		if page > 0 {
			nav = append(nav, telegram.Button{Text: "◀️", CallbackData: "p:" + filter + strconv.Itoa(page-1)})
		}
		nav = append(nav, telegram.Button{Text: fmt.Sprintf("%d / %d", page+1, pages), CallbackData: "p:-"})
		if page < pages-1 {
			nav = append(nav, telegram.Button{Text: "▶️", CallbackData: "p:" + filter + strconv.Itoa(page+1)})
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, nav)
	}
	return sb.String(), kb, nil
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
	case "p":
		_ = b.tg.Answer(ctx, q.ID, "")
		if len(val) < 2 || q.Message == nil {
			return // "p:-" is the page counter
		}
		page, err := strconv.Atoi(val[1:])
		if err != nil {
			return
		}
		text, kb, err := b.listPage(b.st.Lang(q.From.ID), val[:1], page, b.admins[q.From.ID])
		if err != nil {
			log.Printf("db: %v", err)
			return
		}
		if err := b.tg.EditText(ctx, q.Message.Chat.ID, q.Message.MessageID, text, kb); err != nil {
			log.Printf("edit: %v", err)
		}
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
	to, verdict := store.StatusRejected, "rejected"
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
