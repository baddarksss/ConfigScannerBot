package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cfgscanbot/internal/countries"
)

// GeoResult is the outcome of the geo check for one tunnel.
type GeoResult struct {
	OK         bool
	Code       string
	Country    string
	IP         string
	Votes      int
	Answered   int
	DeadTunnel bool
	SingleVote bool
}

// services: {url, countryField, codeField, successField-or-empty}
type service struct {
	url       string
	countryF  string
	codeF     string
	successF  string
}

var services = []service{
	{"https://ipwho.is/", "country", "country_code", "success"},
	{"https://api.country.is/", "country", "country_code", ""},
	{"https://api.ip.sb/geoip", "country", "country_code", ""},
	{"https://ipinfo.io/json", "", "country", ""},
	{"https://www.cloudflare.com/cdn-cgi/trace", "@@trace", "loc", ""},
	// plain HTTP (no TLS): some exits let port-80 traffic through even when
	// they block the TLS geo probes
	{"http://ip-api.com/json/", "country", "countryCode", ""},
}

type failStats struct {
	timeouts int32
	resets   int32
}

func (f *failStats) addTimeout() { atomic.AddInt32(&f.timeouts, 1) }
func (f *failStats) addReset()   { atomic.AddInt32(&f.resets, 1) }

type vote struct {
	code    string
	country string
	ip      string
}

func (v vote) valid() bool { return v != (vote{}) && v.code != "" }

// Check detects the exit country of a local SOCKS5 proxy. Logic is
// identical to the app: lone-request probe (dead-tunnel detection),
// parallel waves with cooldowns, plurality vote, and a final slow retry
// when every attempt timed out.
func Check(ctx context.Context, proxyPort, connectTimeoutSec int, logf func(string)) *GeoResult {
	budget := connectTimeoutSec
	if budget < 30 {
		budget = 30
	}
	if budget > 40 {
		budget = 40
	}
	deadline, cancel := context.WithDeadline(ctx, time.Now().Add(time.Duration(budget)*time.Second))
	defer cancel()

	var votes []vote
	stats := &failStats{}
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)

	// probe: an exit that resets single requests will not carry the bursts
	pk, pv := probeTunnel(deadline, proxyAddr, connectTimeoutSec, stats, logf)
	if pk == pDead {
		return &GeoResult{DeadTunnel: true}
	}
	if pv != nil && pv.valid() {
		votes = append(votes, *pv)
	}

	wave1 := []int{4, 2} // cloudflare, ip.sb
	wave2 := []int{0, 3, 1, 5}
	wave3 := []int{4}
	wave4 := []int{2}
	wave5 := []int{5}

	collectWave(deadline, wave1, proxyPort, connectTimeoutSec, &votes, stats, logf)
	if topVote(votes) >= 2 {
		return makeResult(votes)
	}
	cooldown(deadline)
	collectWave(deadline, wave2, proxyPort, connectTimeoutSec, &votes, stats, logf)
	if topVote(votes) >= 2 {
		return makeResult(votes)
	}
	cooldown(deadline)
	collectWave(deadline, wave3, proxyPort, connectTimeoutSec, &votes, stats, logf)
	if topVote(votes) >= 2 {
		return makeResult(votes)
	}
	cooldown(deadline)
	collectWave(deadline, wave4, proxyPort, connectTimeoutSec, &votes, stats, logf)
	if topVote(votes) >= 2 {
		return makeResult(votes)
	}
	cooldown(deadline)
	collectWave(deadline, wave5, proxyPort, connectTimeoutSec, &votes, stats, logf)
	r := makeResult(votes)

	if !r.OK && atomic.LoadInt32(&stats.timeouts) > 0 && atomic.LoadInt32(&stats.resets) == 0 {
		// everything timed out, nothing reset: slow exit — one last longer
		// lone request before calling it unknown
		proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
		if v := slowRetry(proxyAddr, stats, logf); v.valid() {
			votes = append(votes, v)
			r = makeResult(votes)
		}
	}
	return r
}

func cooldown(deadline context.Context) {
	d, _ := deadline.Deadline()
	left := time.Until(d) - 1200*time.Millisecond
	if left <= 0 {
		return
	}
	if left > time.Second {
		left = time.Second
	}
	select {
	case <-time.After(left):
	case <-deadline.Done():
	}
}

type probeKind int

const (
	pOK probeKind = iota
	pSlow
	pDead
)

type probeOutcome int

const (
	poAnswer probeOutcome = iota
	poTimeout
	poReset
	poNon200
)

// probeTunnel mirrors the app: a TIMEOUT at any attempt means "slow exit"
// (the waves get the chance); only three consecutive RESETS mean dead.
func probeTunnel(ctx context.Context, proxyAddr string, connectTimeoutSec int,
	stats *failStats, logf func(string)) (probeKind, *vote) {
	attempts := []struct {
		idx   int
		parse codeParser
		tag   string
	}{
		{4, traceParse, "probe"},
		{2, jsonCodeParse("country_code"), "probe2"},
		{5, jsonCodeParse("countryCode"), "probe3"},
	}
	for i, a := range attempts {
		out, code := probeOnce(ctx, proxyAddr, connectTimeoutSec, services[a.idx], a.parse, stats, a.tag, logf)
		switch out {
		case poAnswer:
			return pOK, countryVote(code)
		case poTimeout, poNon200:
			return pSlow, nil
		case poReset:
			if logf != nil {
				logf(fmt.Sprintf("geo: %s reset (attempt %d)", a.tag, i+1))
			}
		}
	}
	return pDead, nil
}

type codeParser func(body string) string

func traceParse(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "loc=") {
			code := strings.ToUpper(strings.TrimSpace(line[4:]))
			if len(code) == 2 {
				return code
			}
		}
	}
	return ""
}

func jsonCodeParse(field string) codeParser {
	return func(body string) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			return ""
		}
		if s, ok := m[field].(string); ok {
			s = strings.ToUpper(s)
			if len(s) == 2 {
				return s
			}
		}
		return ""
	}
}

// probeOnce issues one request and classifies the outcome.
func probeOnce(ctx context.Context, proxyAddr string, connectTimeoutSec int,
	svc service, parse codeParser, stats *failStats, tag string, logf func(string)) (probeOutcome, string) {
	client := geoClient(proxyAddr, connectTimeoutSec, 12)
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, svc.url, nil)
	if err != nil {
		stats.addReset()
		return poReset, ""
	}
	resp, err := client.Do(req)
	if err != nil {
		if isTimeout(err) {
			stats.addTimeout()
			return poTimeout, ""
		}
		stats.addReset()
		return poReset, ""
	}
	defer resp.Body.Close()
	if !respOK(resp) {
		return poNon200, ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if code := parse(string(body)); code != "" {
		return poAnswer, code
	}
	return poNon200, ""
}

// slowRetry runs AFTER the geo budget is exhausted, so it uses its own
// deadlines (the run-level ctx is already spent).
func slowRetry(proxyAddr string, stats *failStats, logf func(string)) vote {
	client := geoClientLong(proxyAddr)
	for i, idx := range []int{4, 2} {
		reqCtx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
		v, ok := doQuery(reqCtx, client, services[idx], stats, logf)
		cancel()
		if ok {
			if logf != nil {
				logf("geo: slow retry -> " + v.code)
			}
			return v
		}
		if i == 0 {
			// continue to the second service
		}
	}
	if logf != nil {
		logf("geo: slow retry: no answer")
	}
	return vote{}
}

func collectWave(deadline context.Context, wave []int, proxyPort, connectTimeoutSec int,
	votes *[]vote, stats *failStats, logf func(string)) {
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	var wg sync.WaitGroup
	ch := make(chan vote, len(wave))
	for _, idx := range wave {
		wg.Add(1)
		go func(svc service) {
			defer wg.Done()
			client := geoClient(proxyAddr, connectTimeoutSec, 12)
			v, _ := doQuery(deadline, client, svc, stats, logf)
			ch <- v
		}(services[idx])
	}
	go func() { wg.Wait(); close(ch) }()
	for {
		select {
		case <-deadline.Done():
			return
		case v, ok := <-ch:
			if !ok {
				return
			}
			if v.valid() {
				*votes = append(*votes, v)
			}
		}
	}
}

func doQuery(ctx context.Context, client *http.Client, svc service,
	stats *failStats, logf func(string)) (vote, bool) {
	u, err := url.Parse(svc.url)
	if err != nil {
		stats.addReset()
		return vote{}, false
	}
	resp, err := client.Do(&http.Request{Method: http.MethodGet, URL: u, Host: u.Host})
	if err != nil {
		if isTimeout(err) {
			stats.addTimeout()
		} else {
			stats.addReset()
		}
		if logf != nil {
			logf("geo: " + svc.url + " failed: " + errName(err))
		}
		return vote{}, false
	}
	defer resp.Body.Close()
	if !respOK(resp) {
		if logf != nil {
			logf("geo: " + svc.url + " failed: HTTP " + fmt.Sprint(resp.StatusCode))
		}
		return vote{}, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	text := string(body)
	if svc.countryF == "@@trace" {
		code := traceParse(text)
		if code == "" {
			return vote{}, false
		}
		name, _ := countries.Names(code, "en")
		return vote{code: code, country: name}, true
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		if logf != nil {
			logf("geo: " + svc.url + " failed: bad json")
		}
		return vote{}, false
	}
	if svc.successF != "" {
		if ok, _ := m[svc.successF].(bool); !ok {
			if logf != nil {
				logf("geo: " + svc.url + " failed: success=false")
			}
			return vote{}, false
		}
	}
	country, _ := m[svc.countryF].(string)
	code, _ := m[svc.codeF].(string)
	code = strings.ToUpper(code)
	if code == "" && country == "" {
		if logf != nil {
			logf("geo: " + svc.url + " failed: no country in response")
		}
		return vote{}, false
	}
	if code == "" && country != "" {
		// name-only answer: try to reverse-map is overkill; vote on the name hash
		return vote{code: countryTag(country)}, true
	}
	if country == "" {
		if n, ok := countries.Names(code, "en"); ok {
			country = n
		}
	}
	ip, _ := m["ip"].(string)
	if ip == "" {
		ip, _ = m["query"].(string)
	}
	if logf != nil {
		logf("geo: " + svc.url + " -> " + code + " " + country)
	}
	return vote{code: code, country: country, ip: ip}, true
}

// countryTag gives a deterministic 2-letter-ish bucket for name-only answers.
func countryTag(name string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	if len(name) >= 2 {
		return name[:2]
	}
	return name
}

func topVote(votes []vote) int {
	count := map[string]int{}
	for _, v := range votes {
		if v.code == "" {
			continue
		}
		count[v.code]++
	}
	top := 0
	for _, n := range count {
		if n > top {
			top = n
		}
	}
	return top
}

func makeResult(votes []vote) *GeoResult {
	count := map[string]int{}
	answered := 0
	for _, v := range votes {
		if v.code == "" {
			continue
		}
		answered++
		count[v.code]++
	}
	best, bestN := "", 0
	for c, n := range count {
		if n > bestN {
			bestN, best = n, c
		}
	}
	r := &GeoResult{Answered: answered}
	if best != "" {
		r.OK = true
		r.Code = best
		r.Votes = bestN
		r.SingleVote = bestN < 2
		for _, v := range votes {
			if v.code == best {
				if v.country != "" {
					r.Country = v.country
				}
				if v.ip != "" {
					r.IP = v.ip
				}
			}
		}
	}
	return r
}

func countryVote(code string) *vote {
	name, _ := countries.Names(code, "en")
	return &vote{code: code, country: name}
}

// ------------------------------------------------------------------
// minimal SOCKS5 dialer (stdlib only) + http clients

func respOK(r *http.Response) bool { return r.StatusCode >= 200 && r.StatusCode < 300 }

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

func errName(err error) string {
	if isTimeout(err) {
		return "timeout"
	}
	if errors.Is(err, tls.RecordHeaderError{}) {
		return "TLS reset"
	}
	return err.Error()
}

func socks5Dial(ctx context.Context, proxyAddr, target string) (net.Conn, error) {
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		c.Close()
		return nil, err
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		c.Close()
		return nil, err
	}
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		c.Close()
		return nil, err
	}
	var b2 [2]byte
	if _, err := io.ReadFull(c, b2[:]); err != nil {
		c.Close()
		return nil, err
	}
	if b2[0] != 0x05 || b2[1] != 0x00 {
		c.Close()
		return nil, fmt.Errorf("socks5: method rejected")
	}
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = append(req, byte(port>>8), byte(port&0xff))
	if _, err := c.Write(req); err != nil {
		c.Close()
		return nil, err
	}
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		c.Close()
		return nil, err
	}
	if hdr[1] != 0x00 {
		c.Close()
		return nil, fmt.Errorf("socks5: connect failed code %d", hdr[1])
	}
	var skip int
	switch hdr[3] {
	case 0x01:
		skip = 4
	case 0x03:
		var bl [1]byte
		if _, err := io.ReadFull(c, bl[:]); err != nil {
			c.Close()
			return nil, err
		}
		skip = 1 + int(bl[0])
	case 0x04:
		skip = 16
	default:
		c.Close()
		return nil, fmt.Errorf("socks5: unknown atyp %d", hdr[3])
	}
	rest := make([]byte, skip+2)
	if _, err := io.ReadFull(c, rest); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func geoClient(proxyAddr string, connectTimeoutSec, readTimeoutSec int) *http.Client {
	proxy := proxyAddr
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return socks5Dial(ctx, proxy, addr)
		},
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := socks5Dial(ctx, proxy, addr)
			if err != nil {
				return nil, err
			}
			host, _, _ := net.SplitHostPort(addr)
			tc := tls.Client(c, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
			if err := tc.HandshakeContext(ctx); err != nil {
				c.Close()
				return nil, err
			}
			return tc, nil
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	timeout := time.Duration(connectTimeoutSec+readTimeoutSec) * time.Second
	if timeout < 20*time.Second {
		timeout = 20 * time.Second
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func geoClientLong(proxyAddr string) *http.Client {
	return geoClient(proxyAddr, 8, 20)
}


