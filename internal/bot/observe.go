package bot

import (
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Observability for the poll loop (added in v1.2.3): the bot once went
// "deaf" — alive, but no update was logged for a long stretch, with no
// error line either. The heartbeat + /healthz endpoint make that state
// visible within a minute instead of requiring guesswork.

// runtimeState is a tiny liveness picture of the running bot.
type runtimeState struct {
	startedAt time.Time
	// unix-nano timestamps (0 = not set) — plain atomics, no mutex needed
	pollStarted  atomic.Int64 // when the in-flight getUpdates call began
	lastUpdateID atomic.Int64 // last update id seen by the poll loop
	lastUpdateAt atomic.Int64 // when it was seen
	handled      atomic.Int64 // updates dispatched to handleUpdate
}

func (b *Bot) noteUpdate(id int) {
	b.rs.handled.Add(1)
	b.rs.lastUpdateID.Store(int64(id))
	b.rs.lastUpdateAt.Store(time.Now().UnixNano())
}

func (b *Bot) notePollStart() {
	b.rs.pollStarted.Store(time.Now().UnixNano())
}

// startObservers launches the heartbeat logger and the /healthz server.
// Both are best-effort: they must never take the bot down.
func (b *Bot) startObservers() {
	go b.heartbeatLoop()
	go b.serveHealthz()
}

// heartbeatLoop prints a liveness line every 60s. If this line stops
// appearing in the Railway logs while the container is still up, the poll
// loop itself is wedged. A POLL_STALL marker means a single getUpdates
// call has been in flight for far longer than its client timeout allows.
func (b *Bot) heartbeatLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for range t.C {
		msg := fmt.Sprintf("heartbeat: up=%s handled=%d",
			time.Since(b.rs.startedAt).Round(time.Second), b.rs.handled.Load())
		if id := b.rs.lastUpdateID.Load(); id > 0 {
			age := time.Since(time.Unix(0, b.rs.lastUpdateAt.Load())).Round(time.Second)
			msg += fmt.Sprintf(" lastUpdate=%d (%s ago)", id, age)
		} else {
			msg += " lastUpdate=-"
		}
		if ps := b.rs.pollStarted.Load(); ps > 0 {
			if dur := time.Since(time.Unix(0, ps)); dur > 95*time.Second {
				msg += fmt.Sprintf(" POLL_STALL=%s (getUpdates exceeded its client timeout — poll loop wedged)", dur.Round(time.Second))
			}
		}
		fmt.Println(msg)
	}
}

// serveHealthz exposes a tiny JSON liveness endpoint on $PORT (default
// 8080) so the deployment can be probed from outside without Telegram.
func (b *Bot) serveHealthz() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w,
			`{"ok":true,"version":%q,"uptime_sec":%d,"handled":%d,"last_update_id":%d}`,
			BotVersion, int(time.Since(b.rs.startedAt).Seconds()),
			b.rs.handled.Load(), b.rs.lastUpdateID.Load())
	})
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Println("healthz server stopped:", err)
	}
}

// safeHandle runs one update through handleUpdate with panic recovery —
// a panicking goroutine would otherwise kill the whole process (and the
// bot would silently restart-loop) or lose the update without a trace.
func (b *Bot) safeHandle(u update) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("PANIC handling update %d: %v\n%s\n", u.UpdateID, r, debug.Stack())
		}
	}()
	b.handleUpdate(u)
}
