package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
