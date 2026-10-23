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

	"fogwatch/internal/ai"
	"fogwatch/internal/bot"
	"fogwatch/internal/store"
	"fogwatch/internal/telegram"
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
	if err := run(); err != nil {
		log.Fatal(err) // non-zero exit: Docker restarts the container
	}
}

func run() error {
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
	st, err := store.Open("data/db.sqlite")
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	b := bot.New(
		telegram.New(must("TELEGRAM_TOKEN")),
		st,
		ai.New(
			env("OPENAI_BASE_URL", "https://api.openai.com/v1"),
			must("OPENAI_API_KEY"),
			env("OPENAI_MODEL", "gpt-5-mini"),
		),
		admins,
		loc,
		version,
	)

	// Search every 3 hours, or 6 while searches succeed (see ScheduledFetch).
	// SkipIfStillRunning: a slow search never overlaps.
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	if _, err := c.AddFunc("0 */3 * * *", func() { b.ScheduledFetch(ctx) }); err != nil {
		log.Fatal(err)
	}
	c.Start()
	defer func() { <-c.Stop().Done() }() // wait for a running job before closing the DB

	log.Printf("fogwatch %s running; admins=%d", version, len(admins))
	return b.Run(ctx)
}
