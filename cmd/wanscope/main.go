// wanscope is a bandwidth monitor for OpenWrt routers: per device, per
// uplink, per service, live and over time, with limits and alerts.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/Almo7aya/wanscope/internal/engine"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version")
	verbose := flag.Bool("v", false, "debug logging")
	dataDir := flag.String("data", "", "data directory (overrides the config)")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.Attr{} // procd's log adds the time
		}
		return a
	}}))
	engine.Version = version

	// a router has little memory: collect garbage before the heap gets big
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(96 << 20)

	cfg, err := engine.LoadConfig()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if !cfg.Enabled {
		log.Info("disabled in /etc/config/wanscope")
		return
	}
	e, err := engine.New(log, cfg)
	if err != nil {
		log.Error("start", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := e.Reload(); err != nil {
				log.Warn("reload", "err", err)
			}
		}
	}()
	log.Info("wanscope started", "version", version, "data", cfg.DataDir)
	e.Run(ctx)
}
