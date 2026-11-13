package browserstream

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeOrigin(t *testing.T) {
	ok := []struct{ in, want string }{
		{"https://trainer.example.com", "https://trainer.example.com"},
		{"HTTPS://Trainer.Example.COM", "https://trainer.example.com"},
		{"https://trainer.example.com:443", "https://trainer.example.com"},
		{"http://localhost:80", "http://localhost"},
		{"http://localhost:5173", "http://localhost:5173"},
		{"https://localhost:80", "https://localhost:80"},
		{"http://localhost:443", "http://localhost:443"},
		{"http://127.0.0.1:5173", "http://127.0.0.1:5173"},
		{"http://[::1]:5173", "http://[::1]:5173"},
		{"http://[::1]", "http://[::1]"},
		{"http://[::1]:80", "http://[::1]"},
		{"http://[FE80::1]:8080", "http://[fe80::1]:8080"},
		{"http://a:65535", "http://a:65535"},
		{"http://a:1", "http://a:1"},
	}
	for _, c := range ok {
		got, err := NormalizeOrigin(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	bad := []string{
		"", "null", "*", "https://*", "https://*.example.com", "ftp://a.com", "ws://a.com", "wss://a.com",
		"https://", "https:a.com", "a.com", "//a.com", "https://a.com/", "https://a.com/x", "https://a.com?",
		"https://a.com?x=1", "https://a.com#", "https://a.com#f", "https://u@a.com", "https://u:p@a.com",
		"https://a.com:", "https://a.com:0", "https://a.com:65536", "https://a.com:0443", "https://a.com:x",
		"https://a.com:-1", "https://ex%61mple.com", "https://a b.com", "https://a.com\n", " https://a.com",
		"https://[::1%25lo]:80", "https://[::1", "https://[zz::1]", "https://[127.0.0.1]", "https://a_b.com",
		"https://a.com\\", "https://a.com:80:80",
	}
	for _, in := range bad {
		if got, err := NormalizeOrigin(in); err == nil {
			t.Errorf("NormalizeOrigin(%q) = %q, want error", in, got)
		}
	}
}

func TestValidateToken(t *testing.T) {
	good := []string{strings.Repeat("a", 32), strings.Repeat("Z", 256), "abcdefghijklmnopqrstuvwxyz0123456789-._~ABC"}
	for _, tok := range good {
		if err := ValidateToken(tok); err != nil {
			t.Errorf("ValidateToken(len %d) = %v", len(tok), err)
		}
	}
	bad := []string{"", strings.Repeat("a", 31), strings.Repeat("a", 257), strings.Repeat("a", 31) + " ",
		strings.Repeat("a", 31) + ",", strings.Repeat("a", 31) + "/", strings.Repeat("a", 31) + "é", strings.Repeat("a", 31) + "+"}
	for _, tok := range bad {
		err := ValidateToken(tok)
		if err == nil {
			t.Errorf("ValidateToken(%q) = nil, want error", tok)
			continue
		}
		if tok != "" && strings.Contains(err.Error(), tok) {
			t.Errorf("error %q leaks token", err)
		}
	}
}

func TestParseListen(t *testing.T) {
	cases := []struct {
		in, bind string
		loopback bool
	}{
		{"127.0.0.1:8765", "127.0.0.1:8765", true},
		{"127.9.9.9:1", "127.9.9.9:1", true},
		{"localhost:8765", "127.0.0.1:8765", true},
		{"LocalHost:8765", "127.0.0.1:8765", true},
		{"[::1]:8765", "[::1]:8765", true},
		{"[::ffff:127.0.0.1]:8765", "[::ffff:127.0.0.1]:8765", true},
		{":8765", ":8765", false},
		{"0.0.0.0:8765", "0.0.0.0:8765", false},
		{"[::]:8765", "[::]:8765", false},
		{"192.168.1.10:65535", "192.168.1.10:65535", false},
		{"[fe80::1%eth0]:8765", "[fe80::1%eth0]:8765", false},
		{"[::1%lo]:8765", "[::1%lo]:8765", false},
	}
	for _, c := range cases {
		bind, lb, err := ParseListen(c.in)
		if err != nil || bind != c.bind || lb != c.loopback {
			t.Errorf("ParseListen(%q) = %q, %v, %v; want %q, %v", c.in, bind, lb, err, c.bind, c.loopback)
		}
	}
	for _, in := range []string{"", "8765", "localhost", "localhost.:8765", "example.com:8765", "127.0.0.1",
		"127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:x", "127.0.0.1:+80", "127.1:8765", "[::1]", "::1:8765"} {
		if bind, lb, err := ParseListen(in); err == nil {
			t.Errorf("ParseListen(%q) = %q, %v; want error", in, bind, lb)
		}
	}
}

func TestParseSubprotocols(t *testing.T) {
	tok := strings.Repeat("t", 40)
	cases := []struct {
		name    string
		values  []string
		token   string
		has     bool
		wantErr error
	}{
		{"plain", []string{"gt7.inputs.v1"}, "", false, nil},
		{"with token", []string{"gt7.inputs.v1, gt7.token." + tok}, tok, true, nil},
		{"split lines", []string{"gt7.token." + tok, " gt7.inputs.v1 "}, tok, true, nil},
		{"unknown extra ok", []string{"foo, gt7.inputs.v1"}, "", false, nil},
		{"missing header", nil, "", false, errNoProtocol},
		{"missing proto", []string{"foo"}, "", false, errNoProtocol},
		{"duplicate", []string{"gt7.inputs.v1, gt7.inputs.v1"}, "", false, errProtocolCount},
		{"case variant", []string{"GT7.INPUTS.V1"}, "", false, errProtocolCount},
		{"case dup", []string{"gt7.inputs.v1,GT7.inputs.v1"}, "", false, errProtocolCount},
		{"empty entry", []string{"gt7.inputs.v1,"}, "", false, errEmptyEntry},
		{"empty value", []string{"gt7.inputs.v1", ""}, "", false, errEmptyEntry},
		{"too many", []string{"gt7.inputs.v1,a,b,c,d,e,f,g,h"}, "", false, errTooMany},
		{"eight ok", []string{"gt7.inputs.v1,a,b,c,d,e,f,g"}, "", false, nil},
		{"two tokens", []string{"gt7.inputs.v1,gt7.token.a,gt7.token.b"}, "", false, errTokenCount},
	}
	for _, c := range cases {
		h := http.Header{}
		for _, v := range c.values {
			h.Add("Sec-WebSocket-Protocol", v)
		}
		token, has, err := parseSubprotocols(h)
		if !errors.Is(err, c.wantErr) || token != c.token || has != c.has {
			t.Errorf("%s: got %q, %v, %v; want %q, %v, %v", c.name, token, has, err, c.token, c.has, c.wantErr)
		}
	}
}

func FuzzNormalizeOrigin(f *testing.F) {
	for _, s := range []string{"https://a.com", "http://[::1]:5173", "HTTP://A.COM:80", "null", "https://a.com/",
		"http://x:0443", "https://[fe80::1%25eth0]"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := NormalizeOrigin(in)
		if err != nil {
			return
		}
		again, err := NormalizeOrigin(out)
		if err != nil || again != out {
			t.Fatalf("NormalizeOrigin(%q) = %q, re-normalized to %q, %v", in, out, again, err)
		}
		if p := originPatterns([]string{out}); len(p) == 0 {
			t.Fatalf("no patterns for %q", out)
		}
	})
}

func FuzzParseSubprotocols(f *testing.F) {
	f.Add("gt7.inputs.v1", "gt7.token.abcdefghijklmnopqrstuvwxyz0123456789")
	f.Add("gt7.inputs.v1,gt7.token.x,gt7.token.y", "")
	f.Add(",", " ")
	f.Fuzz(func(t *testing.T, a, b string) {
		h := http.Header{}
		h.Add("Sec-WebSocket-Protocol", a)
		h.Add("Sec-WebSocket-Protocol", b)
		token, has, err := parseSubprotocols(h)
		if err != nil {
			for _, s := range []error{errNoProtocol, errProtocolCount, errEmptyEntry, errTooMany, errTokenCount} {
				if err == s {
					return
				}
			}
			t.Fatalf("non-sentinel error %v", err)
		}
		if !has && token != "" {
			t.Fatalf("token %q without has", token)
		}
		if has && !strings.Contains(a+","+b, tokenPrefix+token) {
			t.Fatalf("token %q not offered", token)
		}
	})
}
