package engine

import (
	"encoding/json"
	"strings"
)

// BuildFull renders the complete xray client config: a local SOCKS5 inbound
// routing everything through the given server — structurally identical to
// the app's XrayConfig.buildFull.
func BuildFull(s *ServerSpec, port int, logPath string) (string, error) {
	log := map[string]any{"loglevel": "info"}
	if logPath != "" {
		log["error"] = logPath
		log["access"] = logPath
	}
	in := map[string]any{
		"tag":    "in",
		"listen": "127.0.0.1",
		"port":   port,
		"protocol": "socks",
		"settings": map[string]any{"udp": false, "auth": "noauth", "ip": "127.0.0.1"},
	}
	out, err := buildOutbound(s)
	if err != nil {
		return "", err
	}
	outMap := out.(map[string]any)
	var outs []any
	var fragOut map[string]any
	if fragSet := FragmentSettings(s.FragmentRaw); fragSet != nil {
		outMap["tag"] = "proxy"
		st, _ := outMap["streamSettings"].(map[string]any)
		if st == nil {
			st = map[string]any{}
			outMap["streamSettings"] = st
		}
		so, _ := st["sockopt"].(map[string]any)
		if so == nil {
			so = map[string]any{}
			st["sockopt"] = so
		}
		so["dialerProxy"] = "fragment"
		so["tcpNoDelay"] = true
		fragOut = map[string]any{
			"tag":        "fragment",
			"protocol":   "freedom",
			"settings":   fragSet,
			"streamSettings": map[string]any{
				"sockopt": map[string]any{"tcpNoDelay": true},
			},
		}
	}
	outs = append(outs, outMap, map[string]any{"protocol": "blackhole", "tag": "block"})
	if fragOut != nil {
		outs = append(outs, fragOut)
	}
	cfg := map[string]any{
		"log":       log,
		"inbounds":  []any{in},
		"outbounds": outs,
	}
	b, err := json.Marshal(cfg)
	return string(b), err
}

// buildOutbound mirrors ServerSpec.buildOutbound.
func buildOutbound(s *ServerSpec) (any, error) {
	stream, err := buildStreamSettings(s)
	if err != nil {
		return nil, err
	}
	switch s.Protocol {
	case "vless":
		user := map[string]any{
			"id":         s.UUID,
			"encryption": firstNonEmpty(s.Encryption, "none"),
		}
		if s.Flow != "" {
			user["flow"] = s.Flow
		}
		return map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": s.Host,
					"port":    s.Port,
					"users":   []any{user},
				}},
			},
			"streamSettings": stream,
		}, nil
	case "vmess":
		return map[string]any{
			"protocol": "vmess",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": s.Host,
					"port":    s.Port,
					"users": []any{map[string]any{
						"id":        s.UUID,
						"alterId":   s.AlterID,
						"security":  firstNonEmpty(s.Cipher, "auto"),
					}},
				}},
			},
			"streamSettings": stream,
		}, nil
	case "trojan":
		return map[string]any{
			"protocol": "trojan",
			"settings": map[string]any{
				"servers": []any{map[string]any{
					"address":  s.Host,
					"port":     s.Port,
					"password": s.Password,
				}},
			},
			"streamSettings": stream,
		}, nil
	case "ss":
		return map[string]any{
			"protocol": "shadowsocks",
			"settings": map[string]any{
				"servers": []any{map[string]any{
					"address":  s.Host,
					"port":     s.Port,
					"method":   s.Method,
					"password": s.Password,
					"uot":      true,
				}},
			},
		}, nil
	case "hysteria2":
		hySni := firstNonEmpty(s.SNI, s.Host)
		tls := map[string]any{
			"serverName":       hySni,
			"curvePreferences": []any{"x25519"},
			"alpn":             []any{"h3"},
		}
		st := map[string]any{
			"network":  "hysteria",
			"security": "tls",
			"hysteriaSettings": map[string]any{
				"version": 2,
				"auth":    s.Password,
			},
			"tlsSettings": tls,
		}
		// salamander obfuscation: type is carried in ExtraRaw, password in Cipher
		if strings.EqualFold(s.ExtraRaw, "salamander") && s.Cipher != "" {
			st["udpmasks"] = []any{map[string]any{
				"type":     "salamander",
				"settings": map[string]any{"password": s.Cipher},
			}}
		}
		return map[string]any{
			"protocol": "hysteria",
			"settings": map[string]any{
				"version": 2,
				"address": s.Host,
				"port":    s.Port,
			},
			"streamSettings": st,
		}, nil
	}
	return nil, &UnsupportedProtocol{s.Protocol}
}

type UnsupportedProtocol struct{ Name string }

func (u *UnsupportedProtocol) Error() string { return "unsupported protocol: " + u.Name }

// buildStreamSettings mirrors ServerSpec.buildStreamSettings.
func buildStreamSettings(s *ServerSpec) (map[string]any, error) {
	st := map[string]any{}
	if s.Network != "" && s.Network != "tcp" {
		st["network"] = s.Network
	}
	if s.Security == "tls" || s.Security == "reality" {
		st["security"] = s.Security
		if s.Security == "reality" {
			r := map[string]any{
				"show":        false,
				"fingerprint": firstNonEmpty(s.Fingerprint, "chrome"),
			}
			if s.SNI != "" {
				r["serverName"] = s.SNI
			}
			if s.PBK != "" {
				r["publicKey"] = s.PBK
			}
			if s.SID != "" {
				r["shortId"] = s.SID
			}
			if s.SPX != "" {
				r["spiderX"] = s.SPX
			}
			st["realitySettings"] = r
		} else {
			t := map[string]any{}
			if s.SNI != "" {
				t["serverName"] = s.SNI
			}
			// classic X25519 only: several servers crash on the post-quantum
			// key share that recent Xray offers by default
			t["curvePreferences"] = []any{"x25519"}
			if s.Fingerprint != "" {
				t["fingerprint"] = s.Fingerprint
			}
			if s.ALPN != "" {
				var a []any
				for _, p := range strings.Split(s.ALPN, ",") {
					if p = strings.TrimSpace(p); p != "" {
						a = append(a, p)
					}
				}
				if len(a) > 0 {
					t["alpn"] = a
				}
			}
		// ECH: panels send a resolver hint (ip.gs+udp://…); xray 26.x
		// "half" = resolve the ECH config itself when advertised
		if s.ECH != "" {
			t["echForceQuery"] = "half"
		}
		// self-signed / insecure=1: pin the leaf cert fetched at test start
		if s.PinnedCert != "" {
			t["pinnedPeerCertSha256"] = s.PinnedCert
		}
		st["tlsSettings"] = t
		}
	}
	if s.Network == "ws" {
		w := map[string]any{}
		if s.Path != "" {
			w["path"] = s.Path
		}
		if s.HostHeader != "" {
			w["headers"] = map[string]any{"Host": s.HostHeader}
		}
		st["wsSettings"] = w
	}
	if s.Network == "grpc" {
		g := map[string]any{"multiMode": false}
		if s.ServiceName != "" {
			g["serviceName"] = s.ServiceName
		}
		st["grpcSettings"] = g
	}
	if s.Network == "xhttp" {
		x := map[string]any{}
		if s.Path != "" {
			x["path"] = s.Path
		}
		if s.HostHeader != "" {
			x["host"] = s.HostHeader
		}
		if s.XhttpMode != "" {
			x["mode"] = s.XhttpMode
		}
		if pad := SanitizePadding(s.XPadBytes); pad != "" {
			x["xPaddingBytes"] = pad
		}
		// merge panel "extra" JSON: xray ignores unknown keys; the xPadding*
		// obfuscation options are real xhttpSettings fields
		if s.ExtraRaw != "" {
			var ex map[string]any
			if err := json.Unmarshal([]byte(s.ExtraRaw), &ex); err == nil {
				for _, k := range []string{"headers", "xmux", "noGRPCHeader",
					"xPaddingObfsMode", "xPaddingKey", "xPaddingHeader", "xPaddingMethod"} {
					if v, ok := ex[k]; ok {
						if _, exists := x[k]; !exists {
							x[k] = v
						}
					}
				}
			}
		}
		st["xhttpSettings"] = x
	}
	return st, nil
}

// FragmentSettings parses the panel ?fm= JSON into xray freedom fragment
// settings, or nil when nothing usable.
func FragmentSettings(raw string) map[string]any {
	var fm map[string]any
	if err := json.Unmarshal([]byte(raw), &fm); err != nil {
		return nil
	}
	arr, ok := fm["tcp"].([]any)
	if !ok {
		return nil
	}
	for _, e := range arr {
		obj, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if obj["type"] != "fragment" {
			continue
		}
		set, _ := obj["settings"].(map[string]any)
		if set == nil {
			continue
		}
		return map[string]any{
			"packets":  strOr(set["packets"], "tlshello"),
			"length":   fragRange(set["lengths"], "50-100"),
			"interval": fragRange(set["delays"], "10-20"),
		}
	}
	return nil
}

func fragRange(v any, dflt string) string {
	arr, ok := v.([]any)
	if !ok {
		return dflt
	}
	if len(arr) >= 2 {
		return utoa(toInt(arr[0])) + "-" + utoa(toInt(arr[1]))
	}
	if len(arr) == 1 {
		n := utoa(toInt(arr[0]))
		return n + "-" + n
	}
	return dflt
}

func toInt(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

func utoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func strOr(v any, dflt string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return dflt
}

// SanitizePadding: xray rejects a padding range whose minimum is 0.
func SanitizePadding(v string) string {
	if v == "" {
		return ""
	}
	i := strings.Index(v, "-")
	if i > 0 {
		n, ok := atoiOK(strings.TrimSpace(v[:i]))
		if ok && n <= 0 {
			return "1" + v[i:]
		}
	}
	return v
}
