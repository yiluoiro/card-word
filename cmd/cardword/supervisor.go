package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/yiluoiro/card-word/internal/config"
	"github.com/yiluoiro/card-word/internal/update"
	"github.com/yiluoiro/card-word/internal/version"
)

const restartExit = 75

var errUpdateRestart = errors.New("restart into staged update")

// Keep a small foreground launcher as PID 1 / the service process. Workers
// may live on the persistent data volume, so read-only images and Windows
// executable locks do not require replacing the running launcher.
func supervise(cfg *config.Config) error {
	launcher, e := os.Executable()
	if e != nil {
		return e
	}
	manager := update.New(cfg.DataDir, version.Version, cfg.MasterKey)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	trial := false
	for ctx.Err() == nil {
		bin, e := manager.Executable(launcher, trial)
		if e == nil {
			e = runWorker(ctx, bin, trial, manager)
		}
		if ctx.Err() != nil {
			return nil
		}
		var exit *exec.ExitError
		if errors.As(e, &exit) && exit.ExitCode() == restartExit {
			trial = true
			continue
		}
		if trial {
			discarded, cleanup := manager.DiscardTrial(bin)
			if cleanup != nil {
				return cleanup
			}
			if discarded {
				fmt.Fprintln(os.Stderr, "cardword: update failed before readiness; restarting previous version:", e)
				trial = false
				continue
			}
		}
		return e
	}
	return nil
}

func runWorker(ctx context.Context, bin string, trial bool, manager *update.Manager) error {
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "CARDWORD_INTERNAL_WORKER=1", fmt.Sprintf("CARDWORD_INTERNAL_TRIAL=%t", trial))
	stdin, e := cmd.StdinPipe()
	if e != nil {
		return e
	}
	defer stdin.Close()
	if e = cmd.Start(); e != nil {
		return e
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ready := !trial
	for {
		select {
		case <-ticker.C:
			if !ready && !manager.TrialPending() {
				ready = true
			}
		case <-timer.C:
			if !ready {
				_ = cmd.Process.Kill()
				<-done
				return errors.New("update startup timed out")
			}
		case e := <-done:
			return e
		case <-ctx.Done():
			stdin.Close()
			select {
			case e := <-done:
				return e
			case <-time.After(28 * time.Second):
				_ = cmd.Process.Kill()
				return <-done
			}
		}
	}
}
