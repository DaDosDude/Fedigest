// Command server boots Fedigest: loads config, opens the database, applies
// migrations, and starts the HTTP server.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fedigest/internal/adapters"
	"fedigest/internal/adapters/lemmy"
	"fedigest/internal/adapters/mbin"
	"fedigest/internal/adapters/piefed"
	"fedigest/internal/config"
	"fedigest/internal/db"
	"fedigest/internal/ingest"
	"fedigest/internal/models"
	"fedigest/internal/web"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] != "backup" || len(os.Args) != 3) {
		fmt.Fprintln(os.Stderr, "usage: fedigest                    start the server")
		fmt.Fprintln(os.Stderr, "       fedigest backup <file.tar.gz>  write a backup archive")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	conn, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer conn.Close()

	if len(os.Args) == 3 {
		if err := runBackup(cfg, conn, os.Args[2]); err != nil {
			log.Fatalf("backup: %v", err)
		}
		log.Printf("backup written to %s", os.Args[2])
		return
	}

	adapters.BlockPrivateNetworks = true
	adapters.HostSpacing = time.Second // one request a second per instance, all of Fedigest together
	// Some instances block generic user agents.
	if cfg.SiteDomain != "" {
		adapters.UserAgent = "Fedigest/1.0 (+https://" + cfg.SiteDomain + ")"
	}

	registry := adapters.NewRegistry()
	registry.Register(models.PlatformLemmy, lemmy.New())
	registry.Register(models.PlatformPieFed, piefed.New())
	registry.Register(models.PlatformMbin, mbin.New())

	srv, err := web.New(cfg, conn, "themes", registry)
	if err != nil {
		log.Fatal(err)
	}
	if err := ensureFirstAdmin(srv.Users, cfg.AdminUsername); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}
	picks, commentSvc := srv.Picks, srv.Comments

	mux := http.NewServeMux()
	mux.Handle("/", srv.Routes())

	// Stopped by `docker compose stop`/`down` (SIGTERM) or Ctrl-C.
	bgCtx, cancelBg := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancelBg()

	// Periodic comment-count refresh for list cards.
	go commentSvc.RunBackgroundCountRefresh(bgCtx, 10*time.Minute)

	poller := ingest.NewPoller(srv.Sources, picks, registry)
	poller.MultiCategory = srv.SiteSettings.MultiCategory
	// A new pick's comment count is fetched at once, ahead of background
	// work in its instance's queue.
	poller.NewPick = func(id int64) {
		p, err := picks.GetByID(id)
		if err != nil {
			return
		}
		if err := commentSvc.RefreshCommentCount(bgCtx, p); err != nil && !adapters.NothingSent(err) {
			log.Printf("comment count for new pick %d: %v", id, err)
		}
	}
	go poller.Run(bgCtx)

	// Scheduled publishing: publish drafts whose publish_at has passed.
	go func() {
		publishDue := func() {
			if n, err := picks.PublishDue(time.Now()); err != nil {
				log.Printf("scheduled publishing: %v", err)
			} else if n > 0 {
				log.Printf("scheduled publishing: published %d pick(s)", n)
			}
		}
		// Expired sessions and reset links are deleted hourly.
		cleanup := func() {
			if err := models.DeleteExpiredLogins(conn, time.Now()); err != nil {
				log.Printf("deleting expired sessions: %v", err)
			}
		}
		publishDue()
		cleanup()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		hourly := time.NewTicker(time.Hour)
		defer hourly.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return
			case <-t.C:
				publishDue()
			case <-hourly.C:
				cleanup()
			}
		}
	}()

	log.Printf("Fedigest listening on %s", cfg.ListenAddr)
	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // uploads
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	select {
	case err := <-serveErr:
		log.Fatal(err)
	case <-bgCtx.Done():
	}

	// Finish requests in flight, then close the database cleanly (which
	// folds its write-ahead log back into the main file).
	log.Printf("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
