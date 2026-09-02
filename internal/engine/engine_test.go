package engine

import (
	"encoding/base64"
	"testing"
)

func TestUniqueNameAndRename(t *testing.T) {
	e := NewEngine(Config{XrayBin: "/bin/true", Channel: "Wpnfa", IncludeChannel: true, OutLang: "fa"})
	if got := e.uniqueName("🇫🇷 فرانسه | Wpnfa"); got != "🇫🇷 فرانسه | Wpnfa" {
		t.Fatal("first should stay clean:", got)
	}
	if got := e.uniqueName("🇫🇷 فرانسه | Wpnfa"); got != "🇫🇷 فرانسه | Wpnfa 2" {
		t.Fatal("second should get counter:", got)
	}
	if got := e.uniqueName("🇫🇷 فرانسه | Wpnfa"); got != "🇫🇷 فرانسه | Wpnfa 3" {
		t.Fatal("third should get 3:", got)
	}
	if got := e.uniqueName("Wpnfa"); got != "Wpnfa" {
		t.Fatal("different base should not collide:", got)
	}
	if got := e.uniqueName("Wpnfa"); got != "Wpnfa 2" {
		t.Fatal("second Wpnfa should counter:", got)
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
