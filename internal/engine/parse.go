package engine

import (
	"encoding/base64"
	"net/url"
	"strings"
)

// ServerSpec is the parsed representation of one proxy config line.
type ServerSpec struct {
	Raw      string
	Protocol string // vless vmess trojan ss hysteria2 ssr tuic shadowtls anytls snic hysteria1
	Name     string
	Host     string
	Port     int

	// vless / shared
	UUID       string
	Flow       string
	Encryption string // vless user encryption (PQC hybrid) or "none"
	XPadBytes  string // xhttp padding, e.g. "100-1000"
	XhttpMode  string // auto | stream-one
	ExtraRaw   string // raw JSON from ?extra=
	ALPN       string
	FragmentRaw string // raw JSON from ?fm=
	ECH        string // e.g. "ip.gs+udp://8.8.8.8"
	Security   string // none | tls | reality
	SNI        string
	Fingerprint string
	PBK        string
	SID        string
	SPX        string
	Network    string // tcp | ws | grpc | xhttp
	Path       string
	HostHeader string
	ServiceName string
	AllowInsecure bool

	// trojan / ss / hy2
	Password string
	Method   string
	Cipher   string
	AlterID  int
}

// Parseable protocols the xray core can actually run; everything else is
// parsed but reported as a skip (same policy as the app).
func (s *ServerSpec) coreSupported() bool {
	switch s.Protocol {
	case "vless", "vmess", "trojan", "ss", "hysteria2":
		return true
	}
	return false
}

func urlDecode(s string) string {
	if s == "" {
		return ""
	}
	v := strings.ReplaceAll(s, "+", "%2B") // literal plus is a plus, not a space
	d, err := url.QueryUnescape(v)
	if err != nil {
		return s
	}
	return d
}

func b64decode(s string) string {
	s = strings.TrimSpace(s)
	std := strings.ReplaceAll(strings.ReplaceAll(s, "-", "+"), "_", "/")
	switch len(std) % 4 {
	case 2:
		std += "=="
	case 3:
		std += "="
	case 1:
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(std); err == nil {
		return string(b)
	}
	urlSafe := strings.ReplaceAll(strings.ReplaceAll(s, "+", "-"), "/", "_")
	for len(urlSafe)%4 != 0 {
		urlSafe += "="
	}
	if b, err := base64.URLEncoding.DecodeString(urlSafe); err == nil {
		return string(b)
	}
	return ""
}

// parseQuery lowercases keys and protects literal '+' before decoding,
// exactly like the app.
func parseQuery(q string) map[string]string {
	m := map[string]string{}
	if q == "" {
		return m
	}
	for _, kv := range strings.Split(q, "&") {
		i := strings.Index(kv, "=")
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(kv[:i]))
		if k == "" {
			continue
		}
		m[k] = urlDecode(kv[i+1:])
	}
	return m
}

func splitHostPort(hp string) (string, string) {
	hp = strings.TrimSpace(hp)
	if hp == "" {
		return "", ""
	}
	if strings.HasPrefix(hp, "[") {
		c := strings.Index(hp, "]")
		if c < 0 {
			return hp, ""
		}
		host := hp[1:c]
		port := ""
		if c+2 < len(hp) && hp[c+1] == ':' {
			port = hp[c+2:]
		}
		return host, port
	}
	c := strings.LastIndex(hp, ":")
	if c < 0 {
		return hp, ""
	}
	// unbracketed IPv6 — the last colon is part of the address
	if strings.Contains(hp[:c], ":") {
		return hp, ""
	}
	maybePort := hp[c+1:]
	if isPort(maybePort) {
		return hp[:c], maybePort
	}
	return hp, ""
}

func isPort(p string) bool {
	if p == "" || len(p) > 5 {
		return false
	}
	n := 0
	for _, ch := range p {
		if ch < '0' || ch > '9' {
			return false
		}
		n = n*10 + int(ch-'0')
	}
	return n <= 65535
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ParseOne parses a single config line; returns nil when unrecognized.
func ParseOne(line string) *ServerSpec {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	l := strings.ToLower(line)
	switch {
	case strings.HasPrefix(l, "vless://"):
		return parseVless(line)
	case strings.HasPrefix(l, "vmess://"):
		return parseVmess(line)
	case strings.HasPrefix(l, "trojan://"):
		return parseTrojan(line)
	case strings.HasPrefix(l, "hysteria2://"), strings.HasPrefix(l, "hy2://"):
		return parseHysteria2(line)
	case strings.HasPrefix(l, "hysteria://"):
		s := &ServerSpec{Raw: line, Protocol: "hysteria1"}
		if fi := strings.LastIndex(line, "#"); fi >= 0 {
			s.Name = urlDecode(line[fi+1:])
		}
		if s.Name == "" {
			s.Name = "hysteria v1"
		}
		return s
	case strings.HasPrefix(l, "ss://"):
		return parseSS(line)
	case strings.HasPrefix(l, "ssr://"):
		return parseSSR(line)
	case strings.HasPrefix(l, "tuic://"):
		return parseTUIC(line)
	case strings.HasPrefix(l, "shadowtls://"):
		return parseShadowTLS(line)
	case strings.HasPrefix(l, "anytls://"):
		return parseAnyTLS(line)
	case strings.HasPrefix(l, "snic://"):
		return parseSNIc(line)
	}
	return nil
}

func takeNameAndBody(s *ServerSpec, line, prefix string) (body string) {
	s.Raw = line
	body = strings.TrimPrefix(line, prefix)
	if fi := strings.LastIndex(body, "#"); fi >= 0 {
		s.Name = urlDecode(body[fi+1:])
		body = body[:fi]
	}
	return body
}

func parseVless(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "vless"}
	body := takeNameAndBody(s, line, "vless://")
	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil
	}
	s.UUID = urlDecode(body[:at])
	rest := body[at+1:]
	var q map[string]string
	hostport := rest
	if qi := strings.Index(rest, "?"); qi >= 0 {
		hostport = rest[:qi]
		q = parseQuery(rest[qi+1:])
	}
	host, portStr := splitHostPort(hostport)
	s.Host = host
	s.Port = 443
	if isPort(portStr) {
		s.Port = atoi(portStr)
	}
	s.Network = firstNonEmpty(q["type"], q["network"], "tcp")
	if s.Network == "h2" || s.Network == "http" {
		s.Network = "tcp"
	}
	s.Security = firstNonEmpty(q["security"], "none")
	s.Flow = q["flow"]
	s.Encryption = q["encryption"]
	s.XPadBytes = firstNonEmpty(q["x_padding_bytes"], q["xpaddingbytes"])
	if m := q["mode"]; m == "auto" || m == "stream-one" {
		s.XhttpMode = m
	}
	s.ExtraRaw = q["extra"]
	s.ALPN = q["alpn"]
	s.FragmentRaw = firstNonEmpty(q["fm"], q["fragment"])
	s.ECH = q["ech"]
	s.SNI = firstNonEmpty(q["sni"], q["servername"], q["peer"])
	s.PBK = q["pbk"]
	s.SID = q["sid"]
	s.SPX = q["spx"]
	s.Fingerprint = firstNonEmpty(q["fp"], "chrome")
	s.Path = q["path"]
	s.HostHeader = q["host"]
	s.ServiceName = q["servicename"]
	if q["insecure"] == "true" || q["insecure"] == "1" ||
		q["allowinsecure"] == "true" || q["allowinsecure"] == "1" {
		s.AllowInsecure = true
	}
	// padding may live inside ?extra=
	if s.XPadBytes == "" && s.ExtraRaw != "" {
		s.XPadBytes = jsonStrField(s.ExtraRaw, "xPaddingBytes", "x_padding_bytes")
	}
	if s.Host == "" || s.UUID == "" {
		return nil
	}
	return s
}

func parseVmess(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "vmess"}
	body := takeNameAndBody(s, line, "vmess://")
	jsonStr := b64decode(body)
	if jsonStr == "" {
		jsonStr = b64decode(urlDecode(body))
	}
	if jsonStr == "" {
		return nil
	}
	s.Host = jsonStrField(jsonStr, "add")
	s.Port = 443
	if p := jsonStrField(jsonStr, "port"); p != "" {
		if n, ok := atoiOK(strings.TrimSpace(p)); ok {
			s.Port = n
		}
	}
	s.UUID = jsonStrField(jsonStr, "id")
	if s.Name == "" {
		s.Name = jsonStrField(jsonStr, "ps")
	}
	s.AlterID = jsonIntField(jsonStr, "aid")
	s.Cipher = firstNonEmpty(jsonStrField(jsonStr, "scy"), "auto")
	s.Network = jsonStrField(jsonStr, "net")
	if s.Network == "" {
		s.Network = "tcp"
	}
	if s.Network == "h2" || s.Network == "http" {
		s.Network = "tcp"
	}
	if jsonStrField(jsonStr, "type") == "none" {
		s.Network = "tcp"
	}
	tls := jsonStrField(jsonStr, "tls")
	if tls == "tls" || tls == "reality" {
		s.Security = tls
	} else {
		s.Security = "none"
	}
	s.SNI = jsonStrField(jsonStr, "sni")
	s.Fingerprint = firstNonEmpty(jsonStrField(jsonStr, "fp"), "chrome")
	s.Path = jsonStrField(jsonStr, "path")
	s.HostHeader = jsonStrField(jsonStr, "host")
	s.Flow = jsonStrField(jsonStr, "flow")
	s.ServiceName = jsonStrField(jsonStr, "servicename")
	if s.ServiceName == "" && s.Network == "grpc" && s.Path != "" {
		s.ServiceName = strings.TrimPrefix(s.Path, "/")
	}
	if s.Host == "" || s.UUID == "" {
		return nil
	}
	return s
}

func parseTrojan(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "trojan"}
	body := takeNameAndBody(s, line, "trojan://")
	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil
	}
	s.Password = urlDecode(body[:at])
	rest := body[at+1:]
	var q map[string]string
	hostport := rest
	if qi := strings.Index(rest, "?"); qi >= 0 {
		hostport = rest[:qi]
		q = parseQuery(rest[qi+1:])
	}
	host, portStr := splitHostPort(hostport)
	s.Host = host
	s.Port = 443
	if isPort(portStr) {
		s.Port = atoi(portStr)
	}
	s.Network = firstNonEmpty(q["type"], "tcp")
	if s.Network == "h2" || s.Network == "http" {
		s.Network = "tcp"
	}
	s.Security = firstNonEmpty(q["security"], "tls")
	s.SNI = firstNonEmpty(q["sni"], q["servername"])
	s.FragmentRaw = firstNonEmpty(q["fm"], q["fragment"])
	s.ECH = q["ech"]
	s.Fingerprint = firstNonEmpty(q["fp"], "chrome")
	s.Path = q["path"]
	s.HostHeader = q["host"]
	s.ServiceName = q["servicename"]
	if q["insecure"] == "true" || q["insecure"] == "1" ||
		q["allowinsecure"] == "true" || q["allowinsecure"] == "1" {
		s.AllowInsecure = true
	}
	if s.Host == "" || s.Password == "" {
		return nil
	}
	return s
}

func parseHysteria2(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "hysteria2", Security: "tls"}
	idx := strings.Index(line, "://")
	body := line[idx+3:]
	if fi := strings.LastIndex(body, "#"); fi >= 0 {
		s.Name = urlDecode(body[fi+1:])
		body = body[:fi]
	}
	s.Raw = line
	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil
	}
	s.Password = urlDecode(body[:at])
	rest := body[at+1:]
	var q map[string]string
	hostport := rest
	if qi := strings.Index(rest, "?"); qi >= 0 {
		hostport = rest[:qi]
		q = parseQuery(rest[qi+1:])
	}
	host, portStr := splitHostPort(hostport)
	s.Host = host
	s.Port = 443
	if isPort(portStr) {
		s.Port = atoi(portStr)
	}
	s.SNI = firstNonEmpty(q["sni"], q["servername"])
	s.Fingerprint = firstNonEmpty(q["fp"], "chrome")
	s.Path = q["path"]
	s.HostHeader = q["host"]
	s.ExtraRaw = q["obfs"] // carried as obfs type
	s.AlterID = 0
	s.Cipher = firstNonEmpty(q["obfsparam"], q["obfspassword"], q["obfs_password"], q["obfs-password"])
	if q["insecure"] == "true" || q["insecure"] == "1" ||
		q["allowinsecure"] == "true" || q["allowinsecure"] == "1" {
		s.AllowInsecure = true
	}
	if s.Host == "" || s.Password == "" {
		return nil
	}
	return s
}

func parseSS(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "ss"}
	body := takeNameAndBody(s, line, "ss://")
	at := strings.LastIndex(body, "@")
	if at >= 0 {
		userinfo := b64decode(body[:at])
		rest := body[at+1:]
		if qi := strings.Index(rest, "?"); qi >= 0 {
			rest = rest[:qi]
		}
		host, portStr := splitHostPort(rest)
		s.Host = host
		if !isPort(portStr) {
			return nil
		}
		s.Port = atoi(portStr)
		ci := strings.Index(userinfo, ":")
		if ci <= 0 {
			return nil
		}
		s.Method = userinfo[:ci]
		s.Password = userinfo[ci+1:]
	} else {
		decoded := b64decode(body)
		parts := strings.Split(decoded, ":")
		if len(parts) < 4 {
			return nil
		}
		s.Method = parts[0]
		pw := parts[1]
		for i := 2; i < len(parts)-2; i++ {
			pw += ":" + parts[i]
		}
		s.Password = pw
		s.Host = parts[len(parts)-2]
		if !isPort(parts[len(parts)-1]) {
			return nil
		}
		s.Port = atoi(parts[len(parts)-1])
	}
	if s.Host == "" || s.Method == "" {
		return nil
	}
	return s
}

// The protocols below are parsed (name/host kept for reporting) but the
// official Xray core does not ship their outbounds — the run marks them
// as skips, exactly like the app.
func parseSSR(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "ssr"}
	body := takeNameAndBody(s, line, "ssr://")
	at := strings.LastIndex(body, "@")
	if at >= 0 {
		ci := strings.Index(body, ":")
		if ci <= 0 {
			return nil
		}
		s.Method = urlDecode(body[:ci])
		rest := body[ci+1:]
		at2 := strings.LastIndex(rest, "@")
		if at2 < 0 {
			return nil
		}
		s.Password = urlDecode(rest[:at2])
		hostport := rest[at2+1:]
		if qi := strings.Index(hostport, "?"); qi >= 0 {
			hostport = hostport[:qi]
		}
		host, portStr := splitHostPort(hostport)
		s.Host = host
		if isPort(portStr) {
			s.Port = atoi(portStr)
		}
	} else {
		decoded := b64decode(body)
		parts := strings.Split(decoded, ":")
		if len(parts) < 4 {
			return nil
		}
		s.Method = parts[0]
		s.Host = parts[len(parts)-2]
		if !isPort(parts[len(parts)-1]) {
			return nil
		}
		s.Port = atoi(parts[len(parts)-1])
		pw := parts[1]
		for i := 2; i < len(parts)-2; i++ {
			pw += ":" + parts[i]
		}
		s.Password = pw
	}
	if s.Host == "" || s.Method == "" {
		return nil
	}
	return s
}

func parseTUIC(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "tuic"}
	body := takeNameAndBody(s, line, "tuic://")
	var q map[string]string
	rest := body
	if qi := strings.Index(body, "?"); qi >= 0 {
		rest = body[:qi]
		q = parseQuery(body[qi+1:])
	}
	hostport := rest
	at := strings.LastIndex(rest, "@")
	if at >= 0 {
		userinfo := rest[:at]
		hostport = rest[at+1:]
		if ci := strings.Index(userinfo, ":"); ci > 0 {
			s.UUID = userinfo[:ci]
		} else {
			s.UUID = userinfo
		}
	}
	host, portStr := splitHostPort(hostport)
	s.Host = host
	if !isPort(portStr) {
		return nil
	}
	s.Port = atoi(portStr)
	if q != nil {
		if u := q["uuid"]; u != "" {
			s.UUID = u
		}
		s.Password = q["password"]
		s.SNI = firstNonEmpty(q["sni"], q["servername"])
		if q["allowinsecure"] == "true" || q["allowinsecure"] == "1" ||
			q["insecure"] == "true" || q["insecure"] == "1" {
			s.AllowInsecure = true
		}
	}
	if s.Host == "" || s.UUID == "" {
		return nil
	}
	return s
}

func parseShadowTLS(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "shadowtls"}
	body := takeNameAndBody(s, line, "shadowtls://")
	var q map[string]string
	rest := body
	if qi := strings.Index(body, "?"); qi >= 0 {
		rest = body[:qi]
		q = parseQuery(body[qi+1:])
	}
	host, portStr := splitHostPort(rest)
	s.Host = host
	if !isPort(portStr) {
		return nil
	}
	s.Port = atoi(portStr)
	s.Password = firstNonEmpty(q["private"], q["password"])
	s.SNI = firstNonEmpty(q["public"], q["sni"])
	s.Path = q["path"]
	s.HostHeader = q["host"]
	if s.Host == "" || s.Password == "" {
		return nil
	}
	return s
}

func parseAnyTLS(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "anytls"}
	body := takeNameAndBody(s, line, "anytls://")
	at := strings.LastIndex(body, "@")
	var pw string
	if at >= 0 {
		pw = urlDecode(body[:at])
		body = body[at+1:]
	}
	var q map[string]string
	hostport := body
	if qi := strings.Index(body, "?"); qi >= 0 {
		hostport = body[:qi]
		q = parseQuery(body[qi+1:])
	}
	host, portStr := splitHostPort(hostport)
	s.Host = host
	if !isPort(portStr) {
		return nil
	}
	s.Port = atoi(portStr)
	s.Password = firstNonEmpty(q["password"], pw)
	s.UUID = q["uuid"]
	s.SNI = firstNonEmpty(q["sni"], q["servername"])
	if s.Host == "" {
		return nil
	}
	return s
}

func parseSNIc(line string) *ServerSpec {
	s := &ServerSpec{Protocol: "snic"}
	body := takeNameAndBody(s, line, "snic://")
	var q map[string]string
	rest := body
	if qi := strings.Index(body, "?"); qi >= 0 {
		rest = body[:qi]
		q = parseQuery(body[qi+1:])
	}
	host, portStr := splitHostPort(rest)
	s.Host = host
	if !isPort(portStr) {
		return nil
	}
	s.Port = atoi(portStr)
	s.SNI = firstNonEmpty(q["sni"], q["servername"])
	if s.Host == "" {
		return nil
	}
	return s
}

func atoi(s string) int {
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

func atoiOK(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + int(ch-'0')
	}
	return n, true
}
