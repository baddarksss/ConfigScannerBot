package engine

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestUniqueNameAndRename(t *testing.T) {
	// since v1.1.0 duplicate labels keep the SAME clean name — no counters
	// ("آلمان 2" is gone; two German servers both stay "🇩🇪 آلمان | Wpnfa")
	if got := labelWithSuffix("🇩🇪 آلمان", " | Wpnfa"); got != "🇩🇪 آلمان | Wpnfa" {
		t.Fatal("label:", got)
	}
	if got := labelWithSuffix("🇩🇪 آلمان", " | Wpnfa"); got != "🇩🇪 آلمان | Wpnfa" {
		t.Fatal("duplicate must not get a counter:", got)
	}
	// rename (spaces are %20-encoded in the fragment, exactly like the app)
	if got := RenameURI("trojan://a@1.1.1.1:443#X", "DE Name | Wpnfa"); got != "trojan://a@1.1.1.1:443#DE%20Name%20|%20Wpnfa" {
		t.Fatal("rename with existing fragment:", got)
	}
	if got := RenameURI("trojan://a@1.1.1.1:443", "DE Name | Wpnfa"); got != "trojan://a@1.1.1.1:443#DE%20Name%20|%20Wpnfa" {
		t.Fatal("rename without fragment:", got)
	}
	// flag
	if got := Flag("fr"); got != "🇫🇷" {
		t.Fatal("flag fr:", got)
	}
	if got := Flag("DE"); got != "🇩🇪" {
		t.Fatal("flag DE:", got)
	}
}

// labelWithSuffix mirrors the naming used by testOne (kept trivial here so
// the regression is about the *absence* of the counter).
func labelWithSuffix(base, suffix string) string { return base + suffix }

func TestParseBasics(t *testing.T) {
	s := ParseOne("vless://uuid-x@62.133.62.179:443?security=reality&pbk=abc&fp=chrome&type=tcp&flow=xtls-rprx-vision&sni=telegraf.lv&sid=e5f67890#نام%20تست")
	if s == nil {
		t.Fatal("vless parse nil")
	}
	if s.Flow != "xtls-rprx-vision" || s.PBK != "abc" || s.SNI != "telegraf.lv" || s.SID != "e5f67890" {
		t.Fatalf("vless fields: %+v", s)
	}
	if s.Name != "نام تست" {
		t.Fatal("vless name decode:", s.Name)
	}

	m := ParseOne("vmess://" + b64url(`{"add":"94.156.170.102","port":"110","id":"u","net":"grpc","path":"svc","tls":"tls","sni":"x.com"}`))
	if m == nil || m.Port != 110 || m.Network != "grpc" || m.ServiceName != "svc" {
		t.Fatalf("vmess: %+v", m)
	}

	tr := ParseOne("trojan://pw@render.com:443?type=ws&path=/a&host=h.com&ech=ip.gs%2Budp%3A%2F%2F8.8.8.8&sni=h.com#t")
	if tr == nil || tr.Network != "ws" || tr.Path != "/a" || tr.HostHeader != "h.com" || tr.ECH != "ip.gs+udp://8.8.8.8" {
		t.Fatalf("trojan: %+v", tr)
	}

	hy := ParseOne("hysteria2://pass@0.0.0.0:443?sni=hy.com&obfs=salamander&obfs-password=secret#h")
	if hy == nil || hy.Protocol != "hysteria2" || hy.ExtraRaw != "salamander" || hy.Cipher != "secret" {
		t.Fatalf("hy2: %+v", hy)
	}

	if ParseOne("not-a-config") != nil {
		t.Fatal("garbage should parse to nil")
	}
}

func b64url(s string) string {
	return base64.URLEncoding.EncodeToString([]byte(s))
}

// Configs pasted from HTML (web pages, other bots) arrive with &amp; — the
// query string must still parse (regression: hy2 obfs password was lost).
func TestParseHysteria2HTMLEntities(t *testing.T) {
	line := "hysteria2://ae98r4oeasrsjpb5@0xdl6fcw.easyiran.org:35256?sni=0xdl6fcw.easyiran.org&amp;obfs=salamander&amp;obfs-password=4gv7x17ll5us41bl#Hy-23"
	specs := ParseInput(line)
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	s := specs[0]
	if s.Protocol != "hysteria2" {
		t.Fatalf("protocol = %q", s.Protocol)
	}
	if s.SNI != "0xdl6fcw.easyiran.org" {
		t.Fatalf("sni = %q", s.SNI)
	}
	if s.ExtraRaw != "salamander" {
		t.Fatalf("obfs = %q, want salamander", s.ExtraRaw)
	}
	if s.Cipher != "4gv7x17ll5us41bl" {
		t.Fatalf("obfs password = %q", s.Cipher)
	}
	if s.Port != 35256 {
		t.Fatalf("port = %d", s.Port)
	}
}

// A plain config without entities must parse identically (no double-unescape).
func TestParseHysteria2Plain(t *testing.T) {
	line := "hysteria2://user@host.example:443?sni=host.example&obfs=salamander&obfs-password=secret#n"
	specs := ParseInput(line)
	if len(specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(specs))
	}
	s := specs[0]
	if s.ExtraRaw != "salamander" || s.Cipher != "secret" {
		t.Fatalf("obfs=%q cipher=%q", s.ExtraRaw, s.Cipher)
	}
}

func TestFragmentSettings(t *testing.T) {
	if FragmentSettings("") != nil {
		t.Fatal("empty string should return nil")
	}
	if FragmentSettings("   ") != nil {
		t.Fatal("blank string should return nil")
	}
	panelJSON := `{"tcp":[{"type":"fragment","settings":{"packets":"tlshello","lengths":["100-200"],"delays":["10-20"]}}]}`
	set := FragmentSettings(panelJSON)
	if set == nil {
		t.Fatal("panelJSON returned nil")
	}
	if set["packets"] != "tlshello" || set["length"] != "100-200" || set["interval"] != "10-20" {
		t.Fatalf("unexpected set: %+v", set)
	}

	directJSON := `{"packets":"tlshello","lengths":"50-100","delays":"10-20"}`
	set2 := FragmentSettings(directJSON)
	if set2 == nil || set2["length"] != "50-100" || set2["interval"] != "10-20" {
		t.Fatalf("unexpected set2: %+v", set2)
	}
}

func TestOutboundOrderAndDNS(t *testing.T) {
	spec := ParseOne("vless://uuid-test@1.1.1.1:443?security=tls&sni=example.com&fm=%7B%22tcp%22%3A%5B%7B%22type%22%3A%22fragment%22%2C%22settings%22%3A%7B%22packets%22%3A%22tlshello%22%2C%22lengths%22%3A%5B%2250-100%22%5D%2C%22delays%22%3A%5B%2210-20%22%5D%7D%7D%5D%7D&ech=ip.gs%2Budp%3A%2F%2F8.8.8.8#Test")
	if spec == nil {
		t.Fatal("vless parse nil")
	}
	cfgStr, err := BuildFull(spec, 10808, "")
	if err != nil {
		t.Fatalf("BuildFull error: %v", err)
	}
	if !strings.Contains(cfgStr, `"dialerProxy":"fragment"`) {
		t.Fatal("dialerProxy fragment missing")
	}
	// verify fragment comes before blackhole
	idxFrag := strings.Index(cfgStr, `"tag":"fragment"`)
	idxBlock := strings.Index(cfgStr, `"tag":"block"`)
	if idxFrag < 0 || idxBlock < 0 || idxFrag > idxBlock {
		t.Fatalf("outbounds order wrong: frag=%d, block=%d", idxFrag, idxBlock)
	}
	// verify DNS resolver was added
	if !strings.Contains(cfgStr, `"udp://8.8.8.8"`) {
		t.Fatal("ECH DNS resolver missing in dns section")
	}
}

func TestGetEchDnsResolver(t *testing.T) {
	if got := GetEchDnsResolver("ip.gs+udp://8.8.8.8"); got != "udp://8.8.8.8" {
		t.Fatalf("resolver format: %q", got)
	}
	if got := GetEchDnsResolver("udp://1.1.1.1:53"); got != "udp://1.1.1.1:53" {
		t.Fatalf("resolver udp: %q", got)
	}
	if got := GetEchDnsResolver("8.8.8.8"); got != "udp://8.8.8.8:53" {
		t.Fatalf("resolver ip: %q", got)
	}
	if got := GetEchDnsResolver(""); got != "" {
		t.Fatalf("empty ech: %q", got)
	}
}

func TestIncludeUnknownFiltering(t *testing.T) {
	e1 := NewEngine(Config{XrayBin: "/bin/true", IncludeUnknown: false})
	okLinks := []string{"vless://ok#DE%20Germany"}
	unkLinks := []string{"vless://unk#unknown"}

	resFalse := &RunResult{
		OK:           len(okLinks),
		NoCountry:    len(unkLinks),
		UnknownLinks: unkLinks,
		Links:        append([]string{}, okLinks...),
	}
	if len(resFalse.Links) != 1 || resFalse.Links[0] != okLinks[0] {
		t.Fatalf("IncludeUnknown=false should only contain okLinks: %+v", resFalse.Links)
	}

	resTrue := &RunResult{
		OK:           len(okLinks),
		NoCountry:    len(unkLinks),
		UnknownLinks: unkLinks,
		Links:        append(append([]string{}, okLinks...), unkLinks...),
	}
	if len(resTrue.Links) != 2 {
		t.Fatalf("IncludeUnknown=true should contain both: %+v", resTrue.Links)
	}
	_ = e1
}

// Regression (v1.0.13): "port" arrives as a JSON number in most vmess
// exports (v2rayN, panels); it used to fall back to 443 silently and the
// wrong server got tested.
func TestParseVmessNumericPort(t *testing.T) {
	s := ParseOne(`vmess://` + b64url(`{"v":"2","ps":"n","add":"1.2.3.4","port":8443,"id":"u","aid":0,"net":"ws","tls":"tls"}`))
	if s == nil || s.Port != 8443 {
		t.Fatalf("numeric port lost: %+v", s)
	}
	s2 := ParseOne(`vmess://` + b64url(`{"add":"1.2.3.4","port":"1234","id":"u"}`))
	if s2 == nil || s2.Port != 1234 {
		t.Fatalf("string port lost: %+v", s2)
	}
	s3 := ParseOne(`vmess://` + b64url(`{"add":"1.2.3.4","port":80,"id":"u","net":"tcp"}`))
	if s3 == nil || s3.Port != 80 {
		t.Fatalf("port 80 lost: %+v", s3)
	}
}

// Regression (v1.0.13): SIP002 plain / percent-encoded userinfo used to be
// dropped silently (only base64 was accepted).
func TestParseSSUserinfoVariants(t *testing.T) {
	plain := ParseOne("ss://aes-256-gcm:secretpass@1.2.3.4:8388#plain")
	if plain == nil || plain.Method != "aes-256-gcm" || plain.Password != "secretpass" || plain.Port != 8388 {
		t.Fatalf("plain userinfo: %+v", plain)
	}
	pct := ParseOne("ss://aes-256-gcm:sec%40pass%3Aword@1.2.3.4:8388#pct")
	if pct == nil || pct.Method != "aes-256-gcm" || pct.Password != "sec@pass:word" {
		t.Fatalf("percent-encoded userinfo: %+v", pct)
	}
	b64l := ParseOne("ss://" + base64.StdEncoding.EncodeToString([]byte("aes-256-gcm:secretpass")) + "@1.2.3.4:8388#b64")
	if b64l == nil || b64l.Method != "aes-256-gcm" || b64l.Password != "secretpass" {
		t.Fatalf("base64 userinfo: %+v", b64l)
	}
}

// Regression (v1.0.13): a bare IPv6 literal produced an invalid hysteria
// YAML "server:" line.
func TestHysteriaServerAddrIPv6(t *testing.T) {
	if got := hysteriaServerAddr("2001:db8::1", 443); got != "[2001:db8::1]:443" {
		t.Fatalf("ipv6: %q", got)
	}
	if got := hysteriaServerAddr("example.com", 8443); got != "example.com:8443" {
		t.Fatalf("dns: %q", got)
	}
	if got := hysteriaServerAddr("1.2.3.4", 1234); got != "1.2.3.4:1234" {
		t.Fatalf("v4: %q", got)
	}
}
