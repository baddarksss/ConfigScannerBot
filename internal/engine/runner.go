package engine

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"html"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cfgscanbot/internal/countries"
)

// Config holds the run options (mirrors the app's settings).
type Config struct {
	XrayBin        string
	HysteriaBin    string // native hysteria client for hy2 (salamander/gecko)
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
	if c.HysteriaBin == "" {
		c.HysteriaBin = "hysteria"
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
// HTML entities are unescaped first: configs pasted from web pages or other
// bots often arrive with "&amp;" instead of "&", which silently mangles the
// query string (e.g. the hy2 obfs password key becomes "amp;obfs-password").
func ParseInput(text string) []*ServerSpec {
	var out []*ServerSpec
	for _, line := range strings.Split(text, "\n") {
		t := html.UnescapeString(strings.TrimSpace(line))
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
	cfg     Config
	mu      sync.Mutex
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
	return &Engine{cfg: cfg}
}

type job struct {
	index int
	spec  *ServerSpec
}

type jobResult struct {
	index int
	line  string
	kind  int
	code  string
}

// Run tests every server using a Worker Pool of cfg.Parallel workers.
// Results and output lines are preserved in exact input order.
func (e *Engine) Run(servers []*ServerSpec, p Progress) *RunResult {
	total := len(servers)
	res := &RunResult{}
	if total == 0 {
		return res
	}

	var (
		portMu   sync.Mutex
		portSet  = map[int]bool{}
		portBase = 21000 + int(time.Now().UnixNano()%500)
	)

	nextPort := func() int {
		portMu.Lock()
		defer portMu.Unlock()
		for i := 0; i < 20000; i++ {
			port := (portBase+i)%60000 + 1024
			if portSet[port] {
				continue
			}
			ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				continue
			}
			ln.Close()
			portSet[port] = true
			return port
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			addr := ln.Addr().(*net.TCPAddr)
			port := addr.Port
			ln.Close()
			portSet[port] = true
			return port
		}
		return 25000
	}
	releasePort := func(port int) {
		portMu.Lock()
		delete(portSet, port)
		portMu.Unlock()
	}

	concurrency := e.cfg.Parallel
	if concurrency > total {
		concurrency = total
	}
	if concurrency <= 0 {
		concurrency = 1
	}

	jobs := make(chan job, total)
	results := make(chan jobResult, total)
	var wg sync.WaitGroup

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				port := nextPort()
				line, kind, code := e.testOne(j.spec, port)
				releasePort(port)
				results <- jobResult{
					index: j.index,
					line:  line,
					kind:  kind,
					code:  code,
				}
			}
		}()
	}

	for i, s := range servers {
		jobs <- job{index: i, spec: s}
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	resultLines := make([]string, total)
	var okLinks []string
	var unkLinks []string
	doneCount := 0

	for r := range results {
		doneCount++
		resultLines[r.index] = r.line
		switch r.kind {
		case kOK:
			res.OK++
			okLinks = append(okLinks, r.line)
			if r.code != "" {
				e.noteCountry(r.code)
			}
		case kPartial:
			res.NoCountry++
			unkLinks = append(unkLinks, r.line)
		case kFail:
			res.Unreachable++
		case kSkip:
			res.Skipped++
		}
		if p != nil {
			mark := "❌"
			switch r.kind {
			case kOK:
				mark = "✅"
			case kPartial:
				mark = "⚠️"
			case kSkip:
				mark = "⏭️"
			}
			p(doneCount, total, servers[r.index].hostport(), mark)
		}
	}

	res.Lines = append(resultLines, "", "—— "+res.Summary(e.cfg.OutLang)+" ——")
	res.UnknownLinks = unkLinks
	res.Links = make([]string, 0, len(okLinks)+len(unkLinks))
	res.Links = append(res.Links, okLinks...)
	if e.cfg.IncludeUnknown {
		res.Links = append(res.Links, unkLinks...)
	}
	res.CountryCodes = e.codes
	res.Flags = e.flags
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

func (e *Engine) testOne(s *ServerSpec, port int) (string, int, string) {
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
		return "⚠️ " + base + " — " + msg, kSkip, ""
	case "ssr", "tuic", "shadowtls", "anytls", "snic":
		msg := s.Protocol + " is not in the official core"
		if cfg.OutLang == "fa" {
			msg = "پروتکل " + s.Protocol + " توی هسته‌ی رسمی نیست"
		}
		return "⚠️ " + base + " — " + msg, kSkip, ""
	case "hysteria2":
		// obfs type without a password cannot be tested at all (app parity)
		obfsType := strings.ToLower(strings.TrimSpace(s.ExtraRaw))
		if (obfsType == "salamander" || obfsType == "gecko") && s.Cipher == "" {
			msg := "hysteria2 obfs without password"
			if cfg.OutLang == "fa" {
				msg = "obfs بدون رمز — قابل تست نیست"
			}
			return "⚠️ " + base + " — " + msg, kSkip, ""
		}
	}

	// insecure=1 with plain TLS: fetch the server's leaf cert and pin it
	// (allowInsecure no longer exists in modern Xray) — app parity
	if s.Protocol != "hysteria2" && s.Security == "tls" && s.AllowInsecure {
		sni := s.SNI
		if sni == "" {
			sni = s.Host
		}
		if h := PinCert(s.Host, s.Port, sni, 8000); h != "" {
			s.PinnedCert = h
			cfg.Logf("test: certpin " + s.hostport() + " sni=" + sni + " hash=" + h)
		} else {
			cfg.Logf("test: certpin FAILED " + s.hostport() + " sni=" + sni)
		}
	}

	// Engine choice: Xray-core's salamander/gecko UDP obfs is broken upstream
	// (its finalmask wrapper never sends or reads packets — verified by the
	// app's packet capture; XTLS/Xray-core#5712 closed as not-planned), so
	// every hysteria2 link runs through the native Hysteria client, exactly
	// like the app. All other protocols use Xray.
	cmd, engineLog, cfgFile := startEngineFor(s, cfg, port)
	if cmd == nil {
		cfg.Logf("test: " + hostport + " engine failed to start")
		return "❌ " + base + " — " + failMsg(cfg.OutLang, "engine"), kFail, ""
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	if cfgFile != "" {
		defer os.Remove(cfgFile)
	}
	defer os.Remove(engineLog)

	time.Sleep(300 * time.Millisecond)

	// wait for the socks port
	waitMs := 3000 + 100*cfg.TimeoutSec
	if waitMs > 8000 {
		waitMs = 8000
	}
	up := waitForPort(port, time.Duration(waitMs)*time.Millisecond)
	if !up {
		cfg.Logf(fmt.Sprintf("test: port %d not up after %dms log=%s",
			port, waitMs, tailFile(engineLog, 200)))
		return "❌ " + base + " — " + failMsg(cfg.OutLang, "connect"), kFail, ""
	}
	cfg.Logf(fmt.Sprintf("test: port %d up", port))

	// geo check
	t0 := time.Now()
	geo := Check(context.Background(), port, cfg.TimeoutSec, cfg.Logf)
	took := time.Since(t0) / time.Second
	cfg.Logf(fmt.Sprintf("test: geo code=%s country=%s ip=%s ok=%v votes=%d took=%ds",
		geo.Code, geo.Country, geo.IP, geo.OK, geo.Votes, took))

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
		// keep the channel suffix on the label end ("🇩🇪 آلمان | Wpnfa").
		// No dedupe counter: duplicate countries keep the same clean name.
		suffix := ""
		if cfg.IncludeChannel && cfg.Channel != "" {
			suffix = " | " + cfg.Channel
		}
		renamed := Flag(geo.Code) + " " + countryName + suffix
		line := RenameURI(s.Raw, renamed)
		cfg.Logf("test: OK " + geo.Code + " -> " + renamed)
		return line, kOK, geo.Code
	}

	// connected but no country — keep the original name (no warning sign);
	// when the config had no name at all, use a neutral word
	baseName := s.Name
	if baseName == "" {
		baseName = "unknown"
		if cfg.OutLang == "fa" {
			baseName = "ناشناس"
		}
	}
	nmSuffix := ""
	if cfg.IncludeChannel && cfg.Channel != "" {
		nmSuffix = " | " + cfg.Channel
	}
	nm := baseName + nmSuffix
	line := RenameURI(s.Raw, nm)
	cfg.Logf("test: PARTIAL (no country) -> " + nm)
	e.mu.Lock()
	e.unknown = append(e.unknown, line)
	e.mu.Unlock()
	return line, kPartial, ""
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

func failMsg(lang, kind string) string {
	if lang == "fa" {
		switch kind {
		case "connect":
			return "اتصال برقرار نشد"
		case "engine":
			return "خطای موتور اسکن — راه‌اندازی ناموفق"
		}
	}
	switch kind {
	case "connect":
		return "connection failed"
	case "engine":
		return "engine failed to start"
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
// startEngineFor launches the right core for this server. It returns the
// process, the engine log path and the written config file ("" for engines
// that need none). A nil process means the engine could not be started.
func startEngineFor(s *ServerSpec, cfg Config, port int) (*exec.Cmd, string, string) {
	if s.Protocol == "hysteria2" {
		return startHysteria(s, cfg, port)
	}
	engineLog := filepath.Join(cfg.WorkDir, "xrayw_"+itoa(port)+".log")
	cfgStr, err := BuildFull(s, port, engineLog)
	if err != nil {
		cfg.Logf("test: build config error: " + err.Error())
		return nil, "", ""
	}
	cfgFile := filepath.Join(cfg.WorkDir, "cfg_"+itoa(port)+".json")
	if err := os.WriteFile(cfgFile, []byte(cfgStr), 0o644); err != nil {
		cfg.Logf("test: write config error: " + err.Error())
		return nil, "", ""
	}
	cmd := exec.Command(cfg.XrayBin, "-c", cfgFile)
	if err := cmd.Start(); err != nil {
		cfg.Logf("test: xray start error: " + err.Error())
		return nil, "", ""
	}
	return cmd, engineLog, cfgFile
}

// startHysteria mirrors the app's HysteriaManager: a YAML client config with
// a local SOCKS5 listener, launched via the native hysteria binary.
func startHysteria(s *ServerSpec, cfg Config, port int) (*exec.Cmd, string, string) {
	engineLog := filepath.Join(cfg.WorkDir, "hy2w_"+itoa(port)+".log")
	var y strings.Builder
	y.WriteString("server: " + hysteriaServerAddr(s.Host, s.Port) + "\n")
	y.WriteString("auth: " + yamlQuote(s.Password) + "\n")
	y.WriteString("tls:\n")
	sni := s.SNI
	if sni == "" {
		sni = s.Host
	}
	y.WriteString("  sni: " + yamlQuote(sni) + "\n")
	if s.AllowInsecure {
		y.WriteString("  insecure: true\n")
	}
	obfsType := strings.ToLower(strings.TrimSpace(s.ExtraRaw))
	if obfsType == "salamander" || obfsType == "gecko" {
		if s.Cipher == "" {
			cfg.Logf("test: hy2 obfs without password")
			return nil, "", ""
		}
		y.WriteString("obfs:\n")
		y.WriteString("  type: " + obfsType + "\n")
		y.WriteString("  " + obfsType + ":\n")
		y.WriteString("    password: " + yamlQuote(s.Cipher) + "\n")
	}
	y.WriteString("socks5:\n")
	fmt.Fprintf(&y, "  listen: 127.0.0.1:%d\n", port)
	cfgFile := filepath.Join(cfg.WorkDir, "hy2_"+itoa(port)+".yaml")
	if err := os.WriteFile(cfgFile, []byte(y.String()), 0o644); err != nil {
		cfg.Logf("test: hy2 write config error: " + err.Error())
		return nil, "", ""
	}
	logF, err := os.OpenFile(engineLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		cfg.Logf("test: hy2 open log error: " + err.Error())
		return nil, "", ""
	}
	cmd := exec.Command(cfg.HysteriaBin, "client", "-c", cfgFile, "-l", "debug")
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		logF.Close()
		cfg.Logf("test: hysteria start error: " + err.Error())
		return nil, "", ""
	}
	// the child keeps the log open; closing our handle is fine
	_ = logF.Close()
	return cmd, engineLog, cfgFile
}

// hysteriaServerAddr formats the server address for the hysteria client
// YAML. A bare IPv6 literal ("2001:db8::1:443") is invalid — it needs
// brackets: "[2001:db8::1]:443".
func hysteriaServerAddr(host string, port int) string {
	if strings.Contains(host, ":") && net.ParseIP(host) != nil {
		return "[" + host + "]:" + itoa(port)
	}
	return host + ":" + itoa(port)
}

func yamlQuote(v string) string {
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + "\""
}

// PinCert mirrors the app's CertPinner: a plain TLS handshake (trust-all for
// this handshake only) to fetch the server's leaf certificate and return its
// SHA-256 hash (lowercase hex) for xray's "pinnedPeerCertSha256" field.
// The actual proxy connection in Xray still verifies the pinned hash strictly.
func PinCert(host string, port int, sni string, timeoutMs int) string {
	cfg := &tls.Config{InsecureSkipVerify: true}
	if sni != "" {
		cfg.ServerName = sni
	}
	d := tls.Dialer{Config: cfg, NetDialer: &net.Dialer{Timeout: time.Duration(timeoutMs) * time.Millisecond}}
	conn, err := d.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return ""
	}
	defer conn.Close()
	tc, ok := conn.(*tls.Conn)
	if !ok {
		return ""
	}
	cs := tc.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	h := sha256.Sum256(cs.PeerCertificates[0].Raw)
	return hex.EncodeToString(h[:])
}

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
