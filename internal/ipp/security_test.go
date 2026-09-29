package ipp

import (
	"net"
	"testing"
)

func TestValidatePrinterURI(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		wantErr bool
	}{
		{"http ok", "http://printer.local:631/ipp/print", false},
		{"ipp ok", "ipp://192.168.1.50:631/ipp/print", false},
		{"ipps ok", "ipps://printer.example.com/ipp/print", false},
		{"loopback allowed by default", "http://127.0.0.1:631/", false},
		{"file scheme rejected", "file:///etc/passwd", true},
		{"gopher scheme rejected", "gopher://127.0.0.1:70/", true},
		{"ftp scheme rejected", "ftp://host/x", true},
		{"empty host rejected", "http://", true},
		{"cloud metadata rejected", "http://169.254.169.254/latest/meta-data/", true},
		{"unspecified rejected", "http://0.0.0.0:631/", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePrinterURI(c.uri)
			if (err != nil) != c.wantErr {
				t.Fatalf("validatePrinterURI(%q) err=%v, wantErr=%v", c.uri, err, c.wantErr)
			}
		})
	}
}

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"169.254.169.254", // 云元数据 (link-local)
		"169.254.1.1",     // link-local
		"fe80::1",         // ipv6 link-local
		"0.0.0.0",         // unspecified
		"224.0.0.1",       // multicast
	}
	for _, s := range blocked {
		if !isBlockedIP(net.ParseIP(s)) {
			t.Errorf("expected %s to be blocked by default", s)
		}
	}

	// 默认策略下环回与私有网段应放行（打印机常在内网）。
	allowed := []string{
		"127.0.0.1",
		"192.168.1.10",
		"10.0.0.5",
		"172.16.3.4",
		"8.8.8.8",
	}
	for _, s := range allowed {
		if isBlockedIP(net.ParseIP(s)) {
			t.Errorf("expected %s to be allowed by default policy", s)
		}
	}

	if !isBlockedIP(nil) {
		t.Errorf("nil IP must be blocked")
	}
}
