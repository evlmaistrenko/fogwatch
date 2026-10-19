package store

// SQLite via GORM. Driver is ncruces/go-sqlite3 (pure Go, no cgo),
// so the binary still builds with CGO_ENABLED=0 for a static image.

import (
	"os"
	"path/filepath"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type Code struct {
	Code      string `gorm:"primaryKey"`
	RewardEN  string `gorm:"not null;default:''"`
	RewardRU  string `gorm:"not null;default:''"`
	Expires   string `gorm:"not null;default:''"` // YYYY-MM-DD, '' = unknown
	Source    string `gorm:"not null"`            // ai | admin
	URL       string `gorm:"not null;default:''"`
	Status    string `gorm:"not null;index"` // pending | published | rejected | removed
	CreatedAt time.Time
}

// Code lifecycle: search finds -> pending (admins only) -> published
// (broadcast) or rejected; /del makes any code removed. Rejected and removed
// are tombstones: search won't re-add them. Codes rejected before the
// rejected status existed are removed.
const (
	StatusPending   = "pending"
	StatusPublished = "published"
	StatusRejected  = "rejected"
	StatusRemoved   = "removed"
)

// Gone reports a tombstone.
func (c *Code) Gone() bool { return c.Status == StatusRejected || c.Status == StatusRemoved }

// User is anyone who pressed /start; all of them get new codes.
// Blocking the bot removes the row.
type User struct {
	ID int64 `gorm:"primaryKey;autoIncrement:false"` // Telegram user id
	// ClientLang follows the Telegram client language (refreshed on every
	// update); Lang is an explicit choice from /lang, '' = follow the client.
	ClientLang string `gorm:"not null;default:'en'"`
	Lang       string `gorm:"not null;default:''"`
	CreatedAt  time.Time
}

// Effective is the language to talk to the user in.
func (u *User) Effective() string {
	if u.Lang != "" {
		return u.Lang
	}
	return u.ClientLang
}

func (c *Code) Active(today string) bool {
	return c.Status == StatusPublished && (c.Expires == "" || c.Expires >= today)
}

func (c *Code) Reward(lang string) string {
	if lang == "ru" && c.RewardRU != "" {
		return c.RewardRU
	}
	return c.RewardEN
}

// Setting is a key-value pair for bot-wide state.
type Setting struct {
	Key   string `gorm:"primaryKey"`
	Value string `gorm:"not null;default:''"`
}

type Store struct{ db *gorm.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // one writer is all SQLite wants; load is trivial
	if err := db.AutoMigrate(&Code{}, &User{}, &Setting{}); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ---------- users ----------

// Touch registers the user if needed, refreshes their client language
// (clientLang "" = unknown, keep the stored one) and returns the effective one.
func (s *Store) Touch(id int64, clientLang string) (string, error) {
	u := User{ID: id, ClientLang: clientLang}
	onConflict := clause.OnConflict{DoNothing: true}
	if clientLang == "" {
		u.ClientLang = "en"
	} else {
		onConflict = clause.OnConflict{DoUpdates: clause.AssignmentColumns([]string{"client_lang"})}
	}
	if err := s.db.Clauses(onConflict).Create(&u).Error; err != nil {
		return "", err
	}
	if err := s.db.Take(&u, id).Error; err != nil {
		return "", err
	}
	return u.Effective(), nil
}

// SetLang sets an explicit language; "" goes back to following the client.
func (s *Store) SetLang(id int64, lang string) error {
	if _, err := s.Touch(id, ""); err != nil {
		return err
	}
	return s.db.Model(&User{ID: id}).Update("lang", lang).Error
}

func (s *Store) DeleteUser(id int64) error {
	return s.db.Delete(&User{}, id).Error
}

// Lang returns a user's effective language, "en" if unknown.
func (s *Store) Lang(id int64) string {
	var u User
	if s.db.Limit(1).Find(&u, id).RowsAffected == 0 {
		return "en"
	}
	return u.Effective()
}

// ChosenLang returns the language picked in /lang, "" if none (follow the client).
func (s *Store) ChosenLang(id int64) string {
	var u User
	s.db.Limit(1).Find(&u, id)
	return u.Lang
}

// ChosenLangs returns id -> language for users who picked one in /lang.
func (s *Store) ChosenLangs() (map[int64]string, error) {
	var us []User
	if err := s.db.Where("lang <> ''").Find(&us).Error; err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(us))
	for _, u := range us {
		m[u.ID] = u.Lang
	}
	return m, nil
}

// Setting returns a stored setting, "" if unset.
func (s *Store) Setting(key string) string {
	var st Setting
	s.db.Limit(1).Find(&st, "key = ?", key)
	return st.Value
}

func (s *Store) SetSetting(key, value string) error {
	return s.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&Setting{Key: key, Value: value}).Error
}

// Users returns id -> language for everyone to broadcast to.
func (s *Store) Users() (map[int64]string, error) {
	var us []User
	if err := s.db.Find(&us).Error; err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(us))
	for _, u := range us {
		m[u.ID] = u.Effective()
	}
	return m, nil
}

// ---------- codes ----------

// Upsert stores a code. Search results (source "ai") start as pending;
// admin-added codes are published right away (also restoring removed ones).
// changed reports a status transition the caller should act on:
// ai -> new pending code (ask admins), admin -> newly published (broadcast).
func (s *Store) Upsert(c Code) (changed bool, err error) {
	want := StatusPending
	if c.Source == "admin" {
		want = StatusPublished
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var old Code
		res := tx.Limit(1).Find(&old, "code = ?", c.Code) // Find: no "record not found" log noise
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			c.Status, changed = want, true
			return tx.Create(&c).Error
		}
		if c.Source != "admin" && old.Gone() {
			return nil
		}
		upd := map[string]any{}
		if c.Source == "admin" {
			upd["source"] = "admin"
			if old.Status != StatusPublished {
				upd["status"], changed = StatusPublished, true
			}
		}
		for col, v := range map[string]string{
			"expires": c.Expires, "reward_en": c.RewardEN, "reward_ru": c.RewardRU, "url": c.URL,
		} {
			if v != "" {
				upd[col] = v
			}
		}
		if len(upd) == 0 {
			return nil
		}
		return tx.Model(&old).Updates(upd).Error
	})
	return changed, err
}

// Moderate moves a pending code to published or rejected. ok=false if the
// code is no longer pending (e.g. another admin was faster); c is then its
// current state, zero if the code doesn't exist.
func (s *Store) Moderate(code, to string) (c Code, ok bool, err error) {
	err = s.db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Code{}).Where("code = ? AND status = ?", code, StatusPending).Update("status", to)
		if res.Error != nil {
			return res.Error
		}
		ok = res.RowsAffected > 0
		return tx.Limit(1).Find(&c, "code = ?", code).Error
	})
	return c, ok, err
}

// Remove tombstones a code. Returns false if it doesn't exist or is already removed.
func (s *Store) Remove(code string) (bool, error) {
	res := s.db.Model(&Code{}).Where("code = ? AND status <> ?", code, StatusRemoved).Update("status", StatusRemoved)
	return res.RowsAffected > 0, res.Error
}

func (s *Store) Pending() ([]Code, error) {
	var cs []Code
	err := s.db.Where("status = ?", StatusPending).Order("created_at").Find(&cs).Error
	return cs, err
}

// Active returns codes valid today, oldest first (so the newest ends up
// at the bottom of the chat when sent one by one).
func (s *Store) Active(today string) ([]Code, error) {
	var cs []Code
	err := s.db.Where("status = ? AND (expires = '' OR expires >= ?)", StatusPublished, today).
		Order("created_at").Find(&cs).Error
	return cs, err
}

// KnownCodes returns up to limit codes of any status (rejected included, so
// search doesn't offer them again), newest first.
func (s *Store) KnownCodes(limit int) ([]string, error) {
	var cs []string
	err := s.db.Model(&Code{}).Order("created_at DESC").Limit(limit).Pluck("code", &cs).Error
	return cs, err
}

// All returns every code, tombstones included, newest first.
func (s *Store) All() ([]Code, error) {
	var cs []Code
	err := s.db.Order("created_at DESC").Find(&cs).Error
	return cs, err
}

// ---------- stats ----------

// Stats is the admin /stats overview. Published codes are split into active
// and expired; Codes holds the stored status counts, so pending, rejected
// and removed come from there.
type Stats struct {
	Users     int
	NewUsers  int            // registered since the given time
	UserLangs map[string]int // effective language -> users
	Codes     map[string]int // status -> codes
	Active    int            // published and not expired
	Expired   int            // published, past their expiry date
	NewCodes  int            // found or added since the given time
	BySource  map[string]int // ai | admin -> codes
}

// TotalCodes counts codes of every status, tombstones included.
func (st *Stats) TotalCodes() int {
	n := 0
	for _, v := range st.Codes {
		n += v
	}
	return n
}

// Stats counts users and codes; "new" means created after since. Counted in
// Go: both tables are small and SQLite time comparisons depend on how the
// driver serialises time.Time.
func (s *Store) Stats(today string, since time.Time) (Stats, error) {
	st := Stats{UserLangs: map[string]int{}, Codes: map[string]int{}, BySource: map[string]int{}}
	var us []User
	if err := s.db.Find(&us).Error; err != nil {
		return st, err
	}
	for _, u := range us {
		st.Users++
		st.UserLangs[u.Effective()]++
		if u.CreatedAt.After(since) {
			st.NewUsers++
		}
	}
	var cs []Code
	if err := s.db.Find(&cs).Error; err != nil {
		return st, err
	}
	for _, c := range cs {
		st.Codes[c.Status]++
		st.BySource[c.Source]++
		if c.CreatedAt.After(since) {
			st.NewCodes++
		}
		if c.Status == StatusPublished {
			if c.Active(today) {
				st.Active++
			} else {
				st.Expired++
			}
		}
	}
	return st, nil
}
