package main

import (
	"fmt"
	"strings"
	"time"

	"cfgscanbot/internal/engine"
)

func main() {
	input := strings.Join([]string{
		"trojan://humanity@130.250.137.171:443?path=%2Fassignment&security=tls&insecure=0&host=www.ignitelimit.com&ech=ip.gs%2Budp%3A%2F%2F8.8.8.8&type=ws&allowInsecure=0&sni=www.ignitelimit.com#test-trojan-a",
		"trojan://humanity@188.114.97.7:443?path=%2Fassignment&security=tls&insecure=0&host=www.ignitelimit.com&ech=ip.gs%2Budp%3A%2F%2F8.8.8.8&type=ws&allowInsecure=0&sni=www.ignitelimit.com#test-trojan-b",
		"vless://d67af820-f54c-48ab-862c-19086357f276@62.133.62.179:443?security=reality&encryption=none&pbk=dhLgVSqPBDrdbhyTS2j60LWZDGEZh-smkcjUNSic-WI&host=%2F%3FBIA_TELEGRAM%40MARAMBASHI_MARAMBASHI&headerType=none&fp=chrome&type=tcp&flow=xtls-rprx-vision&sni=telegraf.lv&sid=e5f67890#test-reality-1",
		"vless://d67af820-f54c-48ab-862c-19086357f276@62.133.62.179:443?security=reality&encryption=none&pbk=dhLgVSqPBDrdbhyTS2j60LWZDGEZh-smkcjUNSic-WI&host=TELEGRAM%40MARAMBASHI_MARAMBASHI&headerType=none&fp=chrome&type=tcp&flow=xtls-rprx-vision&sni=telegraf.lv&sid=e5f67890#test-reality-2",
		"trojan://humanity@render.com:443?path=%2Fassignment&security=tls&insecure=0&host=www.ignitelimit.com&ech=ip.gs%2Budp%3A%2F%2F8.8.8.8&type=ws&allowInsecure=0&sni=www.ignitelimit.com#test-trojan-c",
		"vless://43948542-5c01-4fc3-bf42-e339e9ca3fbe@2.27.27.33:443?mode=auto&path=%2F&security=reality&encryption=none&pbk=B4NK8UZflXK2SI7P6wvCbfSw0wspQRO0ElZM30gc_1E&host=pimg.mycdn.me&fp=random&type=xhttp&sni=pimg.mycdn.me#test-xhttp",
		"hysteria://abc@1.2.3.4:443#hy1-skip-test",
	}, "\n")
	servers := engine.ParseInput(input)
	fmt.Printf("parsed %d servers\n", len(servers))
	for i, s := range servers {
		fmt.Printf("  [%d] %s %s:%d net=%s sec=%s sni=%s name=%q\n",
			i, s.Protocol, s.Host, s.Port, s.Network, s.Security, s.SNI, s.Name)
	}
	cfg := engine.Config{
		XrayBin:    "/tmp/xray/xray",
		WorkDir:    "/tmp/gotest",
		Parallel:   4,
		TimeoutSec: 10,
		OutLang:    "fa",
		Channel:    "TestCh",
		IncludeChannel: true,
		Logf:       func(l string) { fmt.Println("   |", l) },
	}
	eng := engine.NewEngine(cfg)
	t0 := time.Now()
	res := eng.Run(servers, func(d, tot int, hp, mark string) {
		fmt.Printf("PROGRESS %d/%d %s %s\n", d, tot, mark, hp)
	})
	fmt.Printf("\n--- RUN TOOK %.1fs ---\n", time.Since(t0).Seconds())
	fmt.Println("SUMMARY:", res.Summary("fa"))
	fmt.Println("OK/NoCountry/Unreachable/Skipped:", res.OK, res.NoCountry, res.Unreachable, res.Skipped)
	fmt.Println("countries:", res.CountryCodes, "flags:", res.Flags)
	fmt.Println("--- LINES ---")
	for _, l := range res.Lines {
		if len(l) > 160 {
			l = l[:160] + "…"
		}
		fmt.Println("  ", l)
	}
}
