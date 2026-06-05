//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func installGoroutineDumpHandler(ctx context.Context) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				dumpGoroutines()
			}
		}
	}()
}
