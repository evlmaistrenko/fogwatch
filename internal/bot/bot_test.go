package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fogwatch/internal/ai"
	"fogwatch/internal/i18n"
	"fogwatch/internal/store"
	"fogwatch/internal/telegram"
)

func TestCodeMsg(t *testing.T) {
	text, kb := codeMsg("ru", "🆕", store.Code{Code: "AAAA1", RewardEN: "BP", RewardRU: "ОК", Expires: "2030-01-01"})
	if text != "🆕 <code>AAAA1</code> — ОК · до 2030-01-01" {
		t.Fatalf("text: %s", text)
	}
	if b := kb.InlineKeyboard[0][0]; b.CopyText == nil || b.CopyText.Text != "AAAA1" || !strings.HasPrefix(b.Text, "📋") {
		t.Fatalf("button: %+v", b)
	}
}

func TestModCard(t *testing.T) {
	text, kb := modCard("en", store.Code{Code: "AAAA1", RewardEN: "BP", URL: "https://x.test/a?b=1&c=2"})
	if !strings.Contains(text, "https://x.test/a?b=1&amp;c=2") {
		t.Fatalf("url not escaped: %s", text)
	}
	if len(kb.InlineKeyboard) != 2 || kb.InlineKeyboard[1][0].CallbackData != "ok:AAAA1" ||
		kb.InlineKeyboard[1][1].CallbackData != "no:AAAA1" {
		t.Fatalf("keyboard: %+v", kb)
	}
}

func TestScheduledFetchAlerts(t *testing.T) {
	// Fake Telegram: collect sendMessage texts.
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		sent = append(sent, m.Text)
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	// Fake LLM: fails while broken is true.
	broken := true
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"codes\":[]}"}]}]}`))
	}))
	defer llm.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, ai.New(llm.URL, "k", "m"), map[int64]bool{42: true}, time.UTC, "test")
	ctx := context.Background()

	b.ScheduledFetch(ctx) // failure 1: below threshold
	if len(sent) != 0 {
		t.Fatalf("alerted too early: %q", sent)
	}
	b.ScheduledFetch(ctx) // failure 2: alert
	b.ScheduledFetch(ctx) // failure 3: no repeat
	if len(sent) != 1 || !strings.Contains(sent[0], "2 attempts") || strings.Contains(sent[0], "502") {
		t.Fatalf("alert: %q", sent)
	}
	broken = false
	b.ScheduledFetch(ctx) // recovered: one notice
	b.ScheduledFetch(ctx) // still fine: nothing
	if len(sent) != 2 || !strings.Contains(sent[1], "works again") {
		t.Fatalf("recovery: %q", sent)
	}
}

func TestModerationSkipsAdmins(t *testing.T) {
	// Fake Telegram: collect method + chat_id + text.
	type call struct {
		method string
		chat   int64
		text   string
	}
	var calls []call
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			ChatID int64 `json:"chat_id"`
			Text   string
		}
		json.NewDecoder(r.Body).Decode(&m)
		calls = append(calls, call{r.URL.Path[1:], m.ChatID, m.Text})
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, map[int64]bool{42: true}, time.UTC, "test")
	ctx := context.Background()
	st.Touch(42, "en")
	st.Touch(7, "en")

	c := store.Code{Code: "AAAA1", Source: "ai"}
	b.broadcast(ctx, []store.Code{c}, true)
	if len(calls) != 1 || calls[0].chat != 7 {
		t.Fatalf("skipAdmins: %+v", calls)
	}
	calls = nil
	b.broadcast(ctx, []store.Code{c}, false)
	if len(calls) != 2 {
		t.Fatalf("all: %+v", calls)
	}

	// Stale card: the code was already published, the click shows that.
	st.Upsert(c)
	st.Moderate("AAAA1", store.StatusPublished)
	calls = nil
	q := &telegram.CallbackQuery{ID: "q", From: telegram.User{ID: 42}, Message: &telegram.Message{MessageID: 5, Chat: telegram.Chat{ID: 42}}}
	b.onModerate(ctx, q, false, "AAAA1")
	if len(calls) != 2 || calls[1].method != "editMessageText" || !strings.HasSuffix(calls[1].text, "✅ Published") {
		t.Fatalf("stale card: %+v", calls)
	}
}

func TestHelpShowsVersion(t *testing.T) {
	b := New(nil, nil, nil, nil, time.UTC, "1.2.3<x>")
	for _, admin := range []bool{false, true} {
		if h := b.help("en", admin); !strings.HasSuffix(h, "<i>Version 1.2.3&lt;x&gt;</i>") {
			t.Fatalf("admin=%v: %q", admin, h)
		}
	}
}

func TestAdminPlainCodeIsNotAdded(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, map[int64]bool{42: true}, time.UTC, "test")
	msg := func(text string) *telegram.Message {
		return &telegram.Message{From: &telegram.User{ID: 42}, Chat: telegram.Chat{ID: 42, Type: "private"}, Text: text}
	}

	b.onMessage(context.Background(), msg("AAAA1"))
	if all, _ := st.All(); len(all) != 0 {
		t.Fatalf("plain message added a code: %+v", all)
	}
	b.onMessage(context.Background(), msg("/add AAAA1 2099-01-01 Some reward"))
	all, _ := st.All()
	if len(all) != 1 || all[0].Code != "AAAA1" || all[0].RewardEN != "Some reward" {
		t.Fatalf("/add: %+v", all)
	}
	b.onMessage(context.Background(), msg("/add AAAA1 Чары крови | Bloodpoints"))
	if all, _ = st.All(); all[0].RewardRU != "Чары крови" || all[0].RewardEN != "Bloodpoints" {
		t.Fatalf("/add ru|en: %+v", all)
	}
}

func TestChatMenuFollowsChosenLang(t *testing.T) {
	type call struct {
		method, lang, first string
		chat                int64
	}
	var calls []call
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			LanguageCode string `json:"language_code"`
			Scope        struct {
				ChatID int64 `json:"chat_id"`
			}
			Commands []telegram.Cmd `json:"commands"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		c := call{method: r.URL.Path[1:], lang: m.LanguageCode, chat: m.Scope.ChatID}
		if len(m.Commands) > 0 {
			c.first = m.Commands[0].Description
		}
		calls = append(calls, c)
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tg.Close()
	b := New(telegram.NewAt(tg.URL+"/"), nil, nil, map[int64]bool{42: true}, time.UTC, "test")
	ctx := context.Background()

	b.chatMenu(ctx, 7, "en")
	if len(calls) != 1 || calls[0] != (call{"setMyCommands", "", "Active codes", 7}) {
		t.Fatalf("user en: %+v", calls)
	}
	calls = nil
	b.chatMenu(ctx, 7, "")
	if len(calls) != 1 || calls[0] != (call{"deleteMyCommands", "", "", 7}) {
		t.Fatalf("user auto: %+v", calls)
	}

	// Admin: every language variant of the chat menu.
	calls = nil
	b.chatMenu(ctx, 42, "en")
	if len(calls) != len(menuCodes) {
		t.Fatalf("admin en: %+v", calls)
	}
	for _, c := range calls {
		if c.chat != 42 || c.first != "Active codes" {
			t.Fatalf("admin en: %+v", calls)
		}
	}
	calls = nil
	b.chatMenu(ctx, 42, "")
	for _, c := range calls {
		if want := i18n.Commands[menuLang(c.lang)][0].Description; c.first != want {
			t.Fatalf("admin auto %q: got %q, want %q", c.lang, c.first, want)
		}
	}
}

func TestStaleCodesOnlyInList(t *testing.T) {
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		sent = append(sent, m.Text)
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, nil, time.UTC, "test")
	ctx := context.Background()
	old := time.Now().Add(-30 * 24 * time.Hour)

	st.Upsert(store.Code{Code: "OLD01", Source: "admin", CreatedAt: old})
	b.sendFresh(ctx, 7, "en")
	if len(sent) != 1 || !strings.Contains(sent[0], "No new codes in the last 7 days. All active codes (1): /codes") {
		t.Fatalf("only stale: %q", sent)
	}

	// Old but with an expiry date, and new without one: both shown.
	st.Upsert(store.Code{Code: "OLD02", Source: "admin", Expires: "2099-01-01", CreatedAt: old})
	st.Upsert(store.Code{Code: "NEW01", Source: "admin"})
	sent = nil
	b.sendFresh(ctx, 7, "en")
	if len(sent) != 3 || !strings.Contains(sent[0], "OLD02") || !strings.Contains(sent[1], "NEW01") ||
		!strings.HasSuffix(sent[2], "over 7 days ago): 1 — /codes") {
		t.Fatalf("mixed: %q", sent)
	}
}

func TestListPages(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(nil, st, nil, nil, time.UTC, "test")
	if text, kb, err := b.listPage("en", "a", 0, false); err != nil || kb != nil || !strings.Contains(text, "No active codes") {
		t.Fatalf("empty: %q %+v %v", text, kb, err)
	}
	start := time.Now().Add(-time.Hour)
	for i := range 23 {
		st.Upsert(store.Code{Code: fmt.Sprintf("CODE%02d", i), Source: "admin", CreatedAt: start.Add(time.Duration(i) * time.Minute)})
	}

	text, kb, _ := b.listPage("en", "a", 0, false)
	rows := kb.InlineKeyboard
	if !strings.Contains(text, "CODE22") || strings.Contains(text, "CODE12") || len(rows) != 6 {
		t.Fatalf("page 1: %q %+v", text, rows)
	}
	if c := rows[0][0]; c.CopyText == nil || c.CopyText.Text != "CODE22" || len(rows[0]) != 2 {
		t.Fatalf("copy buttons: %+v", rows[0])
	}
	if nav := rows[5]; len(nav) != 2 || nav[0].Text != "1 / 3" || nav[1].CallbackData != "p:a1" {
		t.Fatalf("nav 1: %+v", nav)
	}

	// Last page, also when asked for one past it (codes removed meanwhile).
	for _, p := range []int{2, 9} {
		text, kb, _ = b.listPage("en", "a", p, false)
		rows = kb.InlineKeyboard
		if !strings.Contains(text, "CODE00") || strings.Count(text, "🎁") != 3 || len(rows) != 3 {
			t.Fatalf("page %d: %q %+v", p, text, rows)
		}
		if nav := rows[2]; len(nav) != 2 || nav[0].CallbackData != "p:a1" || nav[1].Text != "3 / 3" {
			t.Fatalf("nav %d: %+v", p, nav)
		}
	}
}

func TestListOrder(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(nil, st, nil, nil, time.UTC, "test")
	old := time.Now().Add(-30 * 24 * time.Hour)
	add := func(code, source, expires string, created time.Time) {
		t.Helper()
		if _, err := st.Upsert(store.Code{Code: code, Source: source, Expires: expires, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	// Insertion order is newest last, as in real life.
	add("STALE", "admin", "", old)
	add("GONE1", "admin", "2000-01-01", old)
	add("LATER", "admin", "2099-12-31", old)
	add("DEL01", "admin", "", old.Add(time.Hour))
	st.Remove("DEL01")
	add("REJ01", "ai", "", old.Add(2*time.Hour))
	st.Moderate("REJ01", store.StatusRejected)
	add("SOON1", "admin", "2099-01-01", time.Time{})
	add("PEND1", "ai", "", time.Time{})
	add("NEW01", "admin", "", time.Time{})

	order := func(admin bool) string {
		t.Helper()
		codes, err := b.listCodes(admin)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range codes {
			names = append(names, c.Code)
		}
		return strings.Join(names, " ")
	}
	if got := order(false); got != "SOON1 LATER NEW01 STALE" {
		t.Fatalf("user: %s", got)
	}
	if got := order(true); got != "SOON1 LATER NEW01 PEND1 REJ01 STALE GONE1 DEL01" {
		t.Fatalf("admin: %s", got)
	}
	text, kb, _ := b.listPage("en", "a", 0, true)
	if f := kb.InlineKeyboard[0]; f[0].Text != "• All 8" || f[1].Text != "🎁 4" || f[1].CallbackData != "p:g0" {
		t.Fatalf("filters: %+v", kb.InlineKeyboard[:2])
	}
	if text, kb, _ := b.listPage("en", "r", 0, true); !strings.Contains(text, "REJ01") || strings.Count(text, "<code>") != 1 ||
		kb.InlineKeyboard[1][0].Text != "• 🚫 1" {
		t.Fatalf("rejected filter: %q %+v", text, kb.InlineKeyboard)
	}
	// Users can't filter: a forged callback shows active codes.
	if text, _, _ := b.listPage("en", "r", 0, false); strings.Contains(text, "REJ01") {
		t.Fatalf("user filter: %q", text)
	}
	for _, want := range []string{"⏳ <code>PEND1", "🚫 <code>REJ01", "🎁 <code>STALE", "⌛ <code>GONE1", "🗑 <code>DEL01"} {
		if !strings.Contains(text, want) {
			t.Fatalf("admin page lacks %q: %s", want, text)
		}
	}
}

func TestStartSetsAdminMenu(t *testing.T) {
	var menus []int64
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/setMyCommands" {
			var m struct {
				Scope struct {
					ChatID int64 `json:"chat_id"`
				}
			}
			json.NewDecoder(r.Body).Decode(&m)
			menus = append(menus, m.Scope.ChatID)
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, map[int64]bool{42: true}, time.UTC, "test")
	start := func(id int64) {
		b.onMessage(context.Background(), &telegram.Message{From: &telegram.User{ID: id}, Chat: telegram.Chat{ID: id, Type: "private"}, Text: "/start"})
	}

	start(7)
	if len(menus) != 0 {
		t.Fatalf("user /start set menus: %v", menus)
	}
	start(42)
	if len(menus) != len(menuCodes) {
		t.Fatalf("admin /start: %v", menus)
	}
	for _, id := range menus {
		if id != 42 {
			t.Fatalf("admin /start: %v", menus)
		}
	}
}

func TestStats(t *testing.T) {
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct{ Text string }
		json.NewDecoder(r.Body).Decode(&m)
		if r.URL.Path == "/sendMessage" {
			sent = append(sent, m.Text)
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, map[int64]bool{42: true}, time.UTC, "test")
	msg := func(from int64) *telegram.Message {
		return &telegram.Message{From: &telegram.User{ID: from, LanguageCode: "en"}, Chat: telegram.Chat{ID: from, Type: "private"}, Text: "/stats"}
	}
	st.Upsert(store.Code{Code: "PEND1", Source: "ai"})
	st.Upsert(store.Code{Code: "LIVE1", Source: "admin"})

	b.onMessage(context.Background(), msg(7))
	if len(sent) != 1 || sent[0] != i18n.T("en", "no_access") {
		t.Fatalf("non-admin: %q", sent)
	}
	sent = nil
	b.onMessage(context.Background(), msg(42))
	if len(sent) != 1 {
		t.Fatalf("admin: %q", sent)
	}
	for _, want := range []string{"Users: 2</b> (admins: 1)", "Codes: 2</b>", "published: 1 (🎁 active 1", "pending: 1", "search 1, admin 1"} {
		if !strings.Contains(sent[0], want) {
			t.Fatalf("missing %q in %q", want, sent[0])
		}
	}
}

func TestRunGivesUpWhenOffline(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := tg.URL
	tg.Close() // connection refused from now on
	b := New(telegram.NewAt(url+"/"), testStore(t), nil, nil, time.UTC, "test")
	b.maxOffline = 0
	if err := b.Run(context.Background()); err == nil {
		t.Fatal("Run returned nil while Telegram was unreachable")
	}

	// Telegram errors mean the network is up: keep polling until cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	var polls atomic.Int32
	tg = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getUpdates" && polls.Add(1) == 2 {
			cancel()
		}
		w.Write([]byte(`{"ok":false,"error_code":409,"description":"Conflict"}`))
	}))
	defer tg.Close()
	b = New(telegram.NewAt(tg.URL+"/"), testStore(t), nil, nil, time.UTC, "test")
	b.maxOffline = 0
	if err := b.Run(ctx); err != nil {
		t.Fatalf("Run on API error: %v", err)
	}
}

func TestSetupMenusStopsWhenOffline(t *testing.T) {
	calls := 0
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`not json`)) // like a proxy error page: not a Telegram answer
	}))
	defer tg.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, map[int64]bool{42: true}, time.UTC, "test")
	if b.setupMenus(context.Background()) {
		t.Fatal("setupMenus reported success after a network error")
	}
	if calls != 1 {
		t.Fatalf("calls after a network error: %d, want 1", calls)
	}
}

func TestMenusRetriedWhenBackOnline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var b *Bot
	var menuCalls atomic.Int32
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getUpdates":
			if b.menus.Load() == menusSet {
				cancel()
			}
			w.Write([]byte(`{"ok":true,"result":[]}`))
		case "/setMyCommands":
			if menuCalls.Add(1) == 1 {
				w.Write([]byte(`not json`)) // offline on the first try
				return
			}
			w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer tg.Close()
	b = New(telegram.NewAt(tg.URL+"/"), testStore(t), nil, nil, time.UTC, "test")
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := menuCalls.Load(), int32(1+len(menuCodes)); got != want {
		t.Fatalf("setMyCommands calls: %d, want %d", got, want)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestUserMenusRefreshedOnChange(t *testing.T) {
	var chats []int64
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct {
			Scope struct {
				ChatID int64 `json:"chat_id"`
			}
		}
		json.NewDecoder(r.Body).Decode(&m)
		if m.Scope.ChatID != 0 {
			chats = append(chats, m.Scope.ChatID)
		}
		w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer tg.Close()
	st := testStore(t)
	st.SetLang(7, "ru")
	st.Touch(8, "en") // follows the client: no chat menu
	b := New(telegram.NewAt(tg.URL+"/"), st, nil, nil, time.UTC, "test")

	if !b.setupMenus(context.Background()) || len(chats) != 1 || chats[0] != 7 {
		t.Fatalf("first start: chat menus %v", chats)
	}
	chats = nil
	if !b.setupMenus(context.Background()) || len(chats) != 0 {
		t.Fatalf("unchanged commands: chat menus %v", chats)
	}
	st.SetSetting(menuSumKey, "old")
	if !b.setupMenus(context.Background()) || len(chats) != 1 {
		t.Fatalf("changed commands: chat menus %v", chats)
	}
}
