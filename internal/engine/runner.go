package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cfgscanbot/internal/countries"
)

// Config holds the run options (mirrors the app's settings).
type Config struct {
	XrayBin        string
	WorkDir        string // temp dir for configs + engine logs
	Parallel       int    // concurrent tests (default 4)
	TimeoutSec     int    // per-server connect timeout (default 10)
	OutLang        string // "fa" or "en" — language of the country name
	Channel        string // channel suffix (e.g. "Wpnfa")
	IncludeChannel bool
	IncludeUnknown bool // count unknown-country links as usable
	Logf           func(string)
}

func (c *Config) defaults() {
	if c.XrayBin == "" {
		c.XrayBin = "xray"
	}
	if c.WorkDir == "" {
		c.WorkDir = os.TempDir()
	}
	if c.Parallel <= 0 {
		c.Parallel = 4
	}
	if c.Parallel > 16 {
		c.Parallel = 16
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 10
	}
	if c.TimeoutSec > 60 {
		c.TimeoutSec = 60
	}
	if c.OutLang != "fa" && c.OutLang != "en" {
		c.OutLang = "fa"
	}
	if c.Logf == nil {
		c.Logf = func(string) {}
	}
}

// RunResult carries everything a UI needs after a run.
type RunResult struct {
	Lines        []string // full output (success/fail/skip lines + summary)
	Links        []string // working links only (unique), incl. unknown per setting
	UnknownLinks []string // unknown-country links (with unique names)
	OK           int
	NoCountry    int
	Unreachable  int
	Skipped      int
	CountryCodes []string // unique ISO codes in detection order (for captions)
	Flags        []string // unique flag emojis in detection order
}

func (r *RunResult) Summary(outLang string) string {
	if outLang == "fa" {
		return fmt.Sprintf("%d موفق · %d بدون شناسایی کشور · %d غیرقابل دسترس · %d ردشده",
			r.OK, r.NoCountry, r.Unreachable, r.Skipped)
	}
	return fmt.Sprintf("%d ok · %d no country · %d unreachable · %d skipped",
		r.OK, r.NoCountry, r.Unreachable, r.Skipped)
}

// ParseInput turns raw pasted text into server specs (skips blanks/comments).
func ParseInput(text string) []*ServerSpec {
	var out []*ServerSpec
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if s := ParseOne(t); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// Progress is called after each finished server: (done, total, hostport, mark).
type Progress func(done, total int, hostport, mark string)

// Engine runs a batch of servers, exactly like the app's run.
type Engine struct {
	cfg Config
	mu  sync.Mutex
	seen    map[string]struct{}
	unknown []string
	codes   []string
	flags   []string
}

// NewEngine prepares a runner.
func NewEngine(cfg Config) *Engine {
	cfg.defaults()
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		cfg.WorkDir = os.TempDir()
	}
	return &Engine{cfg: cfg, seen: map[string]struct{}{}}
}

// Run tests every server with cfg.Parallel concurrent workers.
func (e *Engine) Run(servers []*ServerSpec, p Progress) *RunResult {
	total := len(servers)
	res := &RunResult{}
	var (
		mu        sync.Mutex
		doneCount int
		portSet   = map[int]bool{}
		portBase  = 21000 + int(time.Now().UnixNano()%500)
	)

	nextPort := func() int {
		for i := 0; i < 400; i++ {
			port := (portBase + i)%60000 + 1024
			mu.Lock()
			taken := portSet[port]
			mu.Unlock()
			if taken {
				continue
			}
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				continue
			}
			ln.Close()
			mu.Lock()
			portSet[port] = true
			mu.Unlock()
			return port
		}
		return portBase + 900
	}
	releasePort := func(port int) {
		mu.Lock()
		delete(portSet, port)
		mu.Unlock()
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, e.cfg.Parallel)
	for _, s := range servers {
		wg.Add(1)
		go func(s *ServerSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			port := nextPort()
			defer releasePort(port)
			line, kind := e.testOne(s, port)
			mu.Lock()
			doneCount++
			res.Lines = append(res.Lines, line)
			switch kind {
			case kOK:
				res.OK++
			case kPartial:
				res.NoCountry++
			case kFail:
				res.Unreachable++
			case kSkip:
				res.Skipped++
			}
			d := doneCount
			mu.Unlock()
			if p != nil {
				mark := "❌"
				switch kind {
				case kOK:
					mark = "✅"
				case kPartial:
					mark = "⚠️"
				case kSkip:
					mark = "⏭️"
				}
				p(d, total, s.hostport(), mark)
			}
		}(s)
	}
	wg.Wait()

	mu.Lock()
	res.Lines = append(res.Lines, "", "—— "+res.Summary(e.cfg.OutLang)+" ——")
	res.Links = make([]string, 0, len(res.Lines))
	for _, l := range res.Lines {
		t := strings.TrimSpace(l)
		if isLink(t) {
			res.Links = append(res.Links, t)
		}
	}
	if e.cfg.IncludeUnknown {
		for _, u := range res.UnknownLinks {
			if !contains(res.Links, u) {
				res.Links = append(res.Links, u)
			}
		}
	}
	res.UnknownLinks = e.unknown
	res.CountryCodes = e.codes
	res.Flags = e.flags
	mu.Unlock()
	return res
}

const (
	kOK = iota
	kPartial
	kFail
	kSkip
)

func (s *ServerSpec) hostport() string {
	if s.Port == 0 {
		return s.Host
	}
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

func isLink(t string) bool {
	for _, p := range []string{"vless://", "vmess://", "trojan://", "ss://",
		"socks://", "ssr://", "tuic://", "shadowtls://", "anytls://",
		"snic://", "hysteria2://", "hy2://"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (e *Engine) testOne(s *ServerSpec, port int) (string, int) {
	cfg := e.cfg
	base := s.Name
	if base == "" {
		base = s.hostport()
	}
	hostport := s.hostport()
	cfg.Logf(fmt.Sprintf("test: >> %s %s", s.Protocol, hostport))

	// protocol gates — same policy as the app
	switch s.Protocol {
	case "hysteria1":
		msg := "hysteria v1 is not in the core"
		if cfg.OutLang == "fa" {
			msg = "هیستریا v1 توی هسته پشتیبانی نمی‌شه"
		}
		return "⚠️ " + base + " — " + msg, kSkip
	case "ssr", "tuic", "shadowtls", "anytls", "snic":
		msg := s.Protocol + " is not in the official core"
		if cfg.OutLang == "fa" {
			msg = "پروتکل " + s.Protocol + " توی هسته‌ی رسمی نیست"
		}
		return "⚠️ " + base + " — " + msg, kSkip
	}

	xrayLog := filepath.Join(cfg.WorkDir, "xrayw_"+itoa(port)+".log")
	cfgStr, err := BuildFull(s, port, xrayLog)
	if err != nil {
		return "❌ " + base + " — " + err.Error(), kFail
	}
	cfgFile := filepath.Join(cfg.WorkDir, "cfg_"+itoa(port)+".json")
	if err := os.WriteFile(cfgFile, []byte(cfgStr), 0o644); err != nil {
		return "❌ " + base + " — " + err.Error(), kFail
	}
	defer os.Remove(cfgFile)
	defer os.Remove(xrayLog)

	// launch xray
	cmd := exec.Command(cfg.XrayBin, "-c", cfgFile)
	if err := cmd.Start(); err != nil {
		return "❌ " + base + " — " + "engine start error", kFail
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	time.Sleep(300 * time.Millisecond)

	// wait for the socks port
	waitMs := 3000 + 100*cfg.TimeoutSec
	if waitMs > 8000 {
		waitMs = 8000
	}
	up := waitForPort(port, time.Duration(waitMs)*time.Millisecond)
	if !up {
		cfg.Logf(fmt.Sprintf("test: port %d not up after %dms log=%s",
			port, waitMs, tailFile(xrayLog, 200)))
		return "❌ " + base + " — " + failMsg(cfg.OutLang, "connect"), kFail
	}
	cfg.Logf(fmt.Sprintf("test: port %d up", port))

	// geo check (30-40s budget inside, like the app)
	t0 := time.Now()
	geo := Check(context.Background(), port, cfg.TimeoutSec, cfg.Logf)
	took := time.Since(t0) / time.Second
	cfg.Logf(fmt.Sprintf("test: geo code=%s country=%s ip=%s ok=%v votes=%d took=%ds",
		geo.Code, geo.Country, geo.IP, geo.OK, geo.Votes, took))

	if geo.DeadTunnel {
		cfg.Logf("test: tunnel up but traffic does not pass — " + hostport)
		return "❌ " + base + " — " + failMsg(cfg.OutLang, "dead"), kFail
	}

	if geo.OK && geo.Code != "" {
		countryName := geo.Country
		if countryName == "" {
			countryName = geo.Code
		}
		if cfg.OutLang == "fa" {
			if n, ok := countries.Names(geo.Code, "fa"); ok {
				countryName = n
			}
		}
		suffix := ""
		if cfg.IncludeChannel && cfg.Channel != "" {
			suffix = " | " + cfg.Channel
		}
		renamed := e.uniqueName(Flag(geo.Code) + " " + countryName + suffix)
		line := RenameURI(s.Raw, renamed)
		cfg.Logf("test: OK " + geo.Code + " -> " + renamed)
		e.noteCountry(geo.Code)
		return line, kOK
	}

	// connected but no country — keep the original name (no warning sign),
	// fall back to the channel name when the config had none
	baseName := s.Name
	if baseName == "" {
		if cfg.Channel != "" {
			baseName = cfg.Channel
		} else {
			baseName = s.hostport()
		}
	}
	nm := e.uniqueName(baseName)
	line := RenameURI(s.Raw, nm)
	cfg.Logf("test: PARTIAL (no country) -> " + nm)
	e.mu.Lock()
	e.unknown = append(e.unknown, line)
	e.mu.Unlock()
	return line, kPartial
}

func (e *Engine) noteCountry(code string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, c := range e.codes {
		if c == code {
			return
		}
	}
	e.codes = append(e.codes, code)
	e.flags = append(e.flags, Flag(code))
}

// uniqueName: same policy as the app — duplicate labels get a counter.
func (e *Engine) uniqueName(base string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	cand := base
	n := 2
	for {
		if _, ok := e.seen[cand]; !ok {
			break
		}
		cand = base + " " + itoa(n)
		n++
	}
	e.seen[cand] = struct{}{}
	return cand
}

func failMsg(lang, kind string) string {
	if lang == "fa" {
		switch kind {
		case "connect":
			return "اتصال برقرار نشد"
		case "dead":
			return "وصل شد ولی خروجی ترافیک رو رد نمی‌کنه"
		}
	}
	switch kind {
	case "connect":
		return "connection failed"
	case "dead":
		return "tunnel up but traffic does not pass"
	}
	return "failed"
}

func waitForPort(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func tailFile(path string, maxBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := os.ReadFile(path)
	if len(b) > maxBytes {
		b = b[len(b)-maxBytes:]
	}
	return strings.TrimSpace(string(b))
}

// Flag: ISO 3166-1 alpha-2 -> flag emoji (same as GeoChecker.flag).
func Flag(code string) string {
	if len(code) != 2 {
		return "🏳️"
	}
	c1, c2 := code[0], code[1]
	if c1 >= 'a' && c1 <= 'z' {
		c1 -= 32
	}
	if c2 >= 'a' && c2 <= 'z' {
		c2 -= 32
	}
	if c1 < 'A' || c1 > 'Z' || c2 < 'A' || c2 > 'Z' {
		return "🏳️"
	}
	b := new(strings.Builder)
	b.WriteRune(rune(0x1F1E6) + rune(c1) - 'A')
	b.WriteRune(rune(0x1F1E6) + rune(c2) - 'A')
	return b.String()
}

// RenameURI replaces the last #name segment of the raw URI.
func RenameURI(raw, newName string) string {
	enc := EncodeFragment(newName)
	i := strings.LastIndex(raw, "#")
	if i < 0 {
		return raw + "#" + enc
	}
	return raw[:i+1] + enc
}

// EncodeFragment: percent-encode spaces and control chars only (like the app).
func EncodeFragment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r < 0x20 || r == 0x7f {
			b.WriteString(fmt.Sprintf("%%%02X", r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
