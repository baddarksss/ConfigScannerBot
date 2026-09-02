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
	hyBin := envOr("HYSTERIA_BIN", "hysteria") // native hy2 client (salamander/gecko)

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

	// sanity: xray binary must be present; the hysteria binary is only
	// needed for hy2 links, so a missing one is a warning, not fatal.
	if _, err := os.Stat(xrayBin); err != nil {
		fmt.Fprintf(os.Stderr, "xray binary not found at %s (set XRAY_BIN)\n", xrayBin)
		os.Exit(1)
	}
	if _, err := os.Stat(hyBin); err != nil {
		fmt.Fprintf(os.Stderr, "warning: hysteria binary not found at %s — hy2 links will fail\n", hyBin)
	}

	b := bot.NewBot(token, ownerID, dataDir, xrayBin, hyBin)
	if err := b.Loop(); err != nil {
		fmt.Fprintln(os.Stderr, "bot loop error:", err)
		os.Exit(1)
	}
}
