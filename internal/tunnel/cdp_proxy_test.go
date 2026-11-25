package tunnel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCDPDiscoveryRewriteAndFallback(t *testing.T) {
	// Case 1: Upstream Chrome responds to /json/version -> Host is normalized and port rewritten to listenPort.
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		if r.URL.Path == "/json/version" {
			u, _ := url.Parse("http://" + r.Host)
			_, _ = fmt.Fprintf(w, `{"Browser":"Chrome/144","webSocketDebuggerUrl":"ws://127.0.0.1:%s/devtools/browser/abc"}`, u.Port())
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	chromePort, _ := strconv.Atoi(u.Port())

	c1, c2 := net.Pipe()
	defer c1.Close()

	var active, total, xfer atomic.Int64
	go handleCDPConnection(c2, chromePort, 9222, "/devtools/browser/abc", &active, &total, &xfer, nil, nil)

	_, err := io.WriteString(c1, "GET /json/version HTTP/1.1\r\nHost: 127.0.0.1:9222\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c1), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if gotHost != fmt.Sprintf("127.0.0.1:%d", chromePort) {
		t.Fatalf("expected rewritten Host 127.0.0.1:%d, got %q", chromePort, gotHost)
	}
	if body["webSocketDebuggerUrl"] != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("expected rewritten WS URL on port 9222, got %q", body["webSocketDebuggerUrl"])
	}

	// Case 2: Upstream returns 404 on /json/version (like chrome://inspect minimal mode) -> synthesized JSON returned.
	srv404 := httptest.NewServer(http.NotFoundHandler())
	defer srv404.Close()
	u2, _ := url.Parse(srv404.URL)
	chromePort2, _ := strconv.Atoi(u2.Port())

	c3, c4 := net.Pipe()
	defer c3.Close()
	go handleCDPConnection(c4, chromePort2, 9222, "/devtools/browser/synth-id", &active, &total, &xfer, nil, nil)

	_, _ = io.WriteString(c3, "GET /json/version HTTP/1.1\r\nHost: localhost:9222\r\n\r\n")
	resp2, err := http.ReadResponse(bufio.NewReader(c3), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected synthesized 200 OK, got %d", resp2.StatusCode)
	}
	var body2 map[string]string
	if err := json.NewDecoder(resp2.Body).Decode(&body2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body2["webSocketDebuggerUrl"], "ws://127.0.0.1:9222/devtools/browser/synth-id") {
		t.Fatalf("unexpected synthesized WS URL: %+v", body2)
	}
}
