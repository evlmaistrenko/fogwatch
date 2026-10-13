package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3" // zone database for scratch/distroless images
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func must(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("%s is required", k)
	}
	return v
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	loc, err := time.LoadLocation(env("TZ", "UTC"))
	if err != nil {
		log.Fatal("TZ: ", err)
	}

	admins := map[int64]bool{}
	for _, s := range strings.Split(os.Getenv("ADMIN_IDS"), ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			admins[id] = true
		}
	}

	// Relative to the working dir: ./data locally, /data (the volume) in Docker.
	st, err := OpenStore("data/db.sqlite")
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	b := &Bot{
		tg: NewTG(must("TELEGRAM_TOKEN")),
		st: st,
		ai: NewAI(
			env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			must("OPENAI_API_KEY"),
			env("OPENAI_MODEL", "gpt-5-mini"),
		),
		admins: admins,
		loc:    loc,
	}

	// Command menus: default for everyone, extended for admins. "en" is the
	// fallback for all other client languages. An admin's chat menu fails with
	// "chat not found" until they've pressed /start once; restart after that.
	for lang, cmds := range commands {
		code := lang
		if lang == "en" {
			code = ""
		}
		if err := b.tg.SetCommands(ctx, cmds, code, 0); err != nil {
			log.Printf("setMyCommands %s: %v", lang, err)
		}
		for id := range admins {
			all := append(append([]Cmd{}, cmds...), adminCommands[lang]...)
			if err := b.tg.SetCommands(ctx, all, code, id); err != nil {
				log.Printf("setMyCommands admin %d %s: %v", id, lang, err)
			}
		}
	}

	// Search every 3 hours. SkipIfStillRunning: a slow search never overlaps.
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	if _, err := c.AddFunc("0 */3 * * *", func() { b.ScheduledFetch(ctx) }); err != nil {
		log.Fatal(err)
	}
	c.Start()
	defer func() { <-c.Stop().Done() }() // wait for a running job before closing the DB

	log.Printf("fogwatch %s running; admins=%d", version, len(admins))
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
