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
