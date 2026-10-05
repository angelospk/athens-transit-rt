package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/match"
	"github.com/angelospk/athens-transit-rt/internal/sched"
	"github.com/angelospk/athens-transit-rt/internal/server"
	"github.com/angelospk/athens-transit-rt/internal/telematics"
)

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:8095", "HTTP listen address")
	state := fs.String("state", "/var/lib/atrt", "state directory (snapshot, telematics metadata)")
	rps := fs.Float64("rps", telematics.DefaultRPS, fmt.Sprintf("OASA requests per second, whole process (max %d)", telematics.MaxRPS))
	matcher := fs.String("matcher", "memory", "trip matcher: memory, hungarian or greedy")
	releaseURL := fs.String("release-url", "", "snapshot release base URL (default: this repo's gtfs-snapshot release)")
	gtfsZip := fs.String("gtfs", "", "load this GTFS zip instead of the release snapshot (local use)")
	telURL := fs.String("telematics-url", "", "OASA telematics API base URL")
	check := fs.Duration("release-check", 30*time.Minute, "how often to look for a new snapshot")
	fs.Parse(args)

	kind, ok := match.ParseKind(*matcher)
	if !ok {
		return fmt.Errorf("unknown matcher %q", *matcher)
	}
	if *rps > telematics.MaxRPS {
		return fmt.Errorf("--rps %.1f is above the maximum of %d", *rps, telematics.MaxRPS)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	app := server.New(server.Config{Listen: *listen, StateDir: *state, RPS: *rps, Matcher: kind,
		ReleaseURL: *releaseURL, GTFSZip: *gtfsZip, TelematicsURL: *telURL, ReleaseCheck: *check,
		Sched: sched.DefaultConfig()}, log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := app.Run(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return err
}
