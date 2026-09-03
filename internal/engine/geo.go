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
	"strings"
	"sync"
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
	SingleVote bool
}

// services: {url, countryField, codeField, successField-or-empty}
type service struct {
	url      string
	countryF string
	codeF    string
	successF string
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

type vote struct {
	code    string
	country string
	ip      string
}

func (v vote) valid() bool { return v.code != "" }

// Check detects the exit country of a local SOCKS5 proxy via parallel queries.
func Check(ctx context.Context, proxyPort, connectTimeoutSec int, logf func(string)) *GeoResult {
	if connectTimeoutSec < 5 {
		connectTimeoutSec = 5
	}
	deadline, cancel := context.WithTimeout(ctx, time.Duration(connectTimeoutSec)*time.Second)
	defer cancel()

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", proxyPort)
	client := geoClient(proxyAddr, connectTimeoutSec, connectTimeoutSec)

	var (
		mu       sync.Mutex
		votes    []vote
		voteChan = make(chan vote, len(services))
		done     = make(chan struct{})
		wg       sync.WaitGroup
	)

	for _, svc := range services {
		wg.Add(1)
		go func(s service) {
			defer wg.Done()
			v, ok := doQuery(deadline, client, s, logf)
			if ok && v.valid() {
				select {
				case voteChan <- v:
				case <-deadline.Done():
				}
			}
		}(svc)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	for {
		select {
		case <-deadline.Done():
			mu.Lock()
			r := makeResult(votes)
			mu.Unlock()
			return r
		case <-done:
			for {
				select {
				case v := <-voteChan:
					if v.valid() {
						votes = append(votes, v)
					}
				default:
					return makeResult(votes)
				}
			}
		case v := <-voteChan:
			mu.Lock()
			votes = append(votes, v)
			if topVote(votes) >= 2 {
				r := makeResult(votes)
				mu.Unlock()
				return r
			}
			mu.Unlock()
		}
	}
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

func doQuery(ctx context.Context, client *http.Client, svc service, logf func(string)) (vote, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc.url, nil)
	if err != nil {
		return vote{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return vote{}, false
	}
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
		resolvedCode := countries.CodeByName(country)
		if resolvedCode != "" {
			return vote{code: resolvedCode, country: country}, true
		}
		return vote{}, false
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

	var req []byte
	if ip4 := net.ParseIP(host).To4(); ip4 != nil {
		req = []byte{0x05, 0x01, 0x00, 0x01}
		req = append(req, ip4...)
	} else if ip6 := net.ParseIP(host).To16(); ip6 != nil && strings.Contains(host, ":") {
		req = []byte{0x05, 0x01, 0x00, 0x04}
		req = append(req, ip6...)
	} else {
		req = []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
		req = append(req, host...)
	}
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
		TLSHandshakeTimeout:   time.Duration(connectTimeoutSec) * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	timeout := time.Duration(connectTimeoutSec+readTimeoutSec) * time.Second
	return &http.Client{Transport: tr, Timeout: timeout}
}
