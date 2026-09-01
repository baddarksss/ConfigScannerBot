package main

import (
	"fmt"
	"os"

	"cfgscanbot/internal/bot"
)

func envOr(key, dflt string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return dflt
}

func main() {
	token := os.Getenv("BOT_TOKEN")
	owner := envOr("OWNER_ID", "")
	dataDir := envOr("DATA_DIR", "/data")
	xrayBin := envOr("XRAY_BIN", "xray")

	if token == "" {
		fmt.Fprintln(os.Stderr, "BOT_TOKEN environment variable is required")
		os.Exit(1)
	}
	if owner == "" {
		fmt.Fprintln(os.Stderr, "OWNER_ID environment variable is required (your numeric telegram id)")
		os.Exit(1)
	}
	var ownerID int64
	if _, err := fmt.Sscanf(owner, "%d", &ownerID); err != nil || ownerID <= 0 {
		fmt.Fprintln(os.Stderr, "OWNER_ID must be a numeric telegram user id")
		os.Exit(1)
	}

	// sanity: xray binary must be present
	if _, err := os.Stat(xrayBin); err != nil {
		fmt.Fprintf(os.Stderr, "xray binary not found at %s (set XRAY_BIN)\n", xrayBin)
		os.Exit(1)
	}

	b := bot.NewBot(token, ownerID, dataDir, xrayBin)
	if err := b.Loop(); err != nil {
		fmt.Fprintln(os.Stderr, "bot loop error:", err)
		os.Exit(1)
	}
}
