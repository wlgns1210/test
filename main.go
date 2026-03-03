package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg := parseConfig()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	lt := NewLoadTester(cfg)
	lt.Run(ctx)
	lt.PrintFinalReport()

	if cfg.OutputFile != "" {
		if err := lt.SaveJSON(cfg.OutputFile); err != nil {
			fmt.Fprintf(os.Stderr, "failed to save report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Report saved: %s\n", cfg.OutputFile)
	}
}
