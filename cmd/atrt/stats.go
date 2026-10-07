package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/angelospk/athens-transit-rt/internal/stats"
)

// stats: sum the fix history into daily statistics once (atrt serve --history does it hourly).
func statsCmd(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	hist := fs.String("history", "/var/lib/atrt/history", "fix history directory")
	out := fs.String("out", "/var/lib/atrt/stats", "output directory (days/<date>.json, index.json)")
	force := fs.Bool("force", false, "rewrite days that already have a file")
	fs.Parse(args)
	loc, err := time.LoadLocation(stats.TZ)
	if err != nil {
		return err
	}
	days, err := stats.Run(*hist, *out, loc, time.Now(), *force)
	for _, d := range days {
		fmt.Println("wrote", d)
	}
	return err
}
