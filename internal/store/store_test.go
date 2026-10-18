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
	if _, done, _ := st.Moderate("AAAA1", StatusRemoved); done {
		t.Fatal("moderated twice")
	}
	if a, _, p := count(); a != 1 || p != 0 {
		t.Fatalf("after approve: active=%d pending=%d", a, p)
	}

	// Reject -> tombstone: search can't bring it back, admin can.
	st.Upsert(Code{Code: "BBBB2", Source: "ai"})
	if _, done, _ := st.Moderate("BBBB2", StatusRemoved); !done {
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
	st.SetLang(1, "")
	if touch("en") != "en" {
		t.Fatal("auto not restored")
	}
}
