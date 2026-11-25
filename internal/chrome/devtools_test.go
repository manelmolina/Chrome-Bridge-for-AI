package chrome

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseDevToolsActivePort(t *testing.T) {
	port, wsPath, err := ParseDevToolsActivePort("43821\n/devtools/browser/1234-abcd\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 43821 || wsPath != "/devtools/browser/1234-abcd" {
		t.Fatalf("got (%d, %q)", port, wsPath)
	}

	port, wsPath, err = ParseDevToolsActivePort("\r\n 9222 \r\n /devtools/browser/xyz \r\n")
	if err != nil {
		t.Fatalf("unexpected error with CRLF: %v", err)
	}
	if port != 9222 || wsPath != "/devtools/browser/xyz" {
		t.Fatalf("got (%d, %q)", port, wsPath)
	}

	for _, bad := range []string{
		"",
		"9222",
		"0\n/devtools/browser/x",
		"99999\n/devtools/browser/x",
		"9222\n/devtools/browser/x;rm -rf /",
		"abc\n/devtools/browser/x",
	} {
		if _, _, err := ParseDevToolsActivePort(bad); err == nil {
			t.Fatalf("expected error for %q, got nil", bad)
		}
	}
}

func TestDiscoverLocalChromeFromFile(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	portFile := filepath.Join(dir, "DevToolsActivePort")
	content := fmt.Sprintf("%d\n/devtools/browser/test-uuid\n", port)
	if err := os.WriteFile(portFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	ep, err := DiscoverLocalChrome(dir, 0)
	if err != nil {
		t.Fatalf("expected active endpoint, got %v", err)
	}
	if ep.Port != port || ep.WSPath != "/devtools/browser/test-uuid" || ep.SourceType != "DevToolsActivePort" {
		t.Fatalf("unexpected endpoint: %+v", ep)
	}
}

func TestDiscoverLocalChromeFallbackHTTP(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			http.NotFound(w, r)
			return
		}
		u, _ := url.Parse(srv.URL)
		_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/browser/http-id"}`, u.Port())
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	p, _ := strconv.Atoi(u.Port())

	ep, err := DiscoverLocalChrome(t.TempDir(), p)
	if err != nil {
		t.Fatalf("expected fallback endpoint, got %v", err)
	}
	if ep.Port != p || ep.WSPath != "/devtools/browser/http-id" || ep.SourceType != "json/version" {
		t.Fatalf("unexpected fallback endpoint: %+v", ep)
	}
}
