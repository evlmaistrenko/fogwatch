package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ok := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC().Format(time.DateOnly)

	count := func() (active, all, pending int) {
		t.Helper()
		a, err := st.Active(today)
		ok(err)
		l, err := st.All()
		ok(err)
		p, err := st.Pending()
		ok(err)
		return len(a), len(l), len(p)
	}

	// Search result -> pending, not visible to users.
	changed, err := st.Upsert(Code{Code: "AAAA1", Source: "ai", RewardEN: "BP", RewardRU: "ОК"})
	ok(err)
	if !changed {
		t.Fatal("new ai code: expected changed")
	}
	if changed, _ := st.Upsert(Code{Code: "AAAA1", Source: "ai"}); changed {
		t.Fatal("dup reported as changed")
	}
	if a, l, p := count(); a != 0 || l != 1 || p != 1 {
		t.Fatalf("after ai: active=%d all=%d pending=%d", a, l, p)
	}

	// Approve -> published; second click is a no-op.
	c, done, err := st.Moderate("AAAA1", StatusPublished)
	ok(err)
	if !done || c.Status != StatusPublished || c.RewardRU != "ОК" {
		t.Fatalf("approve: %v %+v", done, c)
	}
	if c, done, _ := st.Moderate("AAAA1", StatusRejected); done || c.Status != StatusPublished {
		t.Fatalf("moderated twice: %v %+v", done, c)
	}
	if c, done, err := st.Moderate("NONE0", StatusRejected); err != nil || done || c.Code != "" {
		t.Fatalf("missing code: %v %v %+v", err, done, c)
	}
	if a, _, p := count(); a != 1 || p != 0 {
		t.Fatalf("after approve: active=%d pending=%d", a, p)
	}

	// Reject -> tombstone: search can't bring it back, admin can.
	st.Upsert(Code{Code: "BBBB2", Source: "ai"})
	if _, done, _ := st.Moderate("BBBB2", StatusRejected); !done {
		t.Fatal("reject failed")
	}
	if changed, _ := st.Upsert(Code{Code: "BBBB2", Source: "ai"}); changed {
		t.Fatal("ai resurrected rejected code")
	}
	if changed, _ := st.Upsert(Code{Code: "BBBB2", Source: "admin"}); !changed {
		t.Fatal("admin restore failed")
	}

	// Admin code is published at once; expired ones aren't active.
	if changed, _ := st.Upsert(Code{Code: "OLD01", Source: "admin", Expires: "2000-01-01"}); !changed {
		t.Fatal("admin add: expected changed")
	}
	if a, l, _ := count(); a != 2 || l != 3 {
		t.Fatalf("final: active=%d all=%d", a, l)
	}
	known, err := st.KnownCodes(10)
	ok(err)
	if len(known) != 3 { // pending/published/removed alike
		t.Fatalf("known: %v", known)
	}
	removed, err := st.Remove("AAAA1")
	ok(err)
	if !removed {
		t.Fatal("remove failed")
	}
	if changed, _ := st.Upsert(Code{Code: "AAAA1", Source: "ai"}); changed {
		t.Fatal("ai resurrected removed code")
	}
	if all, _ := st.All(); len(all) != 3 {
		t.Fatalf("all with tombstones: %+v", all)
	}

	// Users + persistence.
	_, err = st.Touch(1, "ru")
	ok(err)
	_, err = st.Touch(2, "en")
	ok(err)
	ok(st.DeleteUser(2))
	ok(st.Close())
	st, err = Open(path)
	ok(err)
	users, err := st.Users()
	ok(err)
	if len(users) != 1 || users[1] != "ru" || st.Lang(1) != "ru" || st.Lang(99) != "en" {
		t.Fatalf("users: %v", users)
	}
}

func TestLang(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	touch := func(client string) string {
		t.Helper()
		l, err := st.Touch(1, client)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if touch("") != "en" || touch("ru") != "ru" || touch("") != "ru" {
		t.Fatal("follow client / keep on unknown")
	}
	if touch("en") != "en" {
		t.Fatal("client change not followed")
	}
	st.SetLang(1, "ru")
	if touch("en") != "ru" || st.Lang(1) != "ru" {
		t.Fatal("explicit choice not kept")
	}
	st.Touch(2, "en")
	if m, err := st.ChosenLangs(); err != nil || len(m) != 1 || m[1] != "ru" {
		t.Fatalf("ChosenLangs: %v %v", m, err)
	}
	st.SetLang(1, "")
	if touch("en") != "en" {
		t.Fatal("auto not restored")
	}
	if m, _ := st.ChosenLangs(); len(m) != 0 {
		t.Fatalf("ChosenLangs after auto: %v", m)
	}
}

func TestSetting(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	if v := st.Setting("k"); v != "" {
		t.Fatalf("unset: %q", v)
	}
	for _, v := range []string{"a", "b"} {
		if err := st.SetSetting("k", v); err != nil {
			t.Fatal(err)
		}
		if got := st.Setting("k"); got != v {
			t.Fatalf("got %q, want %q", got, v)
		}
	}
}

func TestStats(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Touch(1, "ru")
	st.Touch(2, "en")
	st.Touch(3, "en")
	st.SetLang(3, "ru")
	st.Upsert(Code{Code: "PEND1", Source: "ai"})
	st.Upsert(Code{Code: "REJE1", Source: "ai"})
	st.Moderate("REJE1", StatusRejected)
	st.Upsert(Code{Code: "LIVE1", Source: "admin"})
	st.Upsert(Code{Code: "LIVE2", Source: "admin", Expires: "2099-01-01"})
	st.Upsert(Code{Code: "OLD01", Source: "admin", Expires: "2000-01-01"})
	st.Upsert(Code{Code: "GONE1", Source: "admin"})
	st.Remove("GONE1")

	today := time.Now().UTC().Format(time.DateOnly)
	s, err := st.Stats(today, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Users != 3 || s.NewUsers != 3 || s.UserLangs["ru"] != 2 || s.UserLangs["en"] != 1 {
		t.Fatalf("users: %+v", s)
	}
	if s.TotalCodes() != 6 || s.NewCodes != 6 || s.Codes[StatusPending] != 1 || s.Codes[StatusRejected] != 1 ||
		s.Codes[StatusPublished] != 3 || s.Codes[StatusRemoved] != 1 || s.Active != 2 || s.Expired != 1 {
		t.Fatalf("codes: %+v", s)
	}
	if s.BySource["ai"] != 2 || s.BySource["admin"] != 4 {
		t.Fatalf("sources: %+v", s.BySource)
	}
	if s, _ := st.Stats(today, time.Now().Add(time.Hour)); s.NewUsers != 0 || s.NewCodes != 0 {
		t.Fatalf("new since the future: %+v", s)
	}
}
