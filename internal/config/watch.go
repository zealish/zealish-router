package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// settleDelay coalesces the burst of events a single save produces. Editors
// routinely emit CHMOD + WRITE, or RENAME + CREATE for atomic replacement.
const settleDelay = 150 * time.Millisecond

// Watch calls onChange with a freshly loaded configuration every time the file
// at path changes. Invalid configurations are reported to onError and skipped,
// leaving the caller on its previous config. It blocks until ctx is done.
//
// The parent directory is watched rather than the file itself: atomic saves
// (write temp + rename) replace the inode, which would silently detach a
// file-level watch.
func Watch(ctx context.Context, path string, logger *slog.Logger, onChange func(*Config)) error {
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = watcher.Close() }()

	if err := watcher.Add(filepath.Dir(target)); err != nil {
		return err
	}
	logger.Info("watching configuration", slog.String("path", target))

	// A nil channel blocks forever in select, so the timer arms only once an
	// event has actually landed.
	var settle <-chan time.Time
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(event.Name) != target {
				continue
			}
			if event.Has(fsnotify.Chmod) {
				continue
			}
			timer.Reset(settleDelay)
			settle = timer.C

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Warn("config watch error", slog.Any("error", err))

		case <-settle:
			settle = nil

			cfg, err := Load(target)
			if err != nil {
				logger.Error("config reload rejected, keeping previous configuration",
					slog.String("path", target),
					slog.Any("error", err))
				continue
			}
			onChange(cfg)
		}
	}
}
