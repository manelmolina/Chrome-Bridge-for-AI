package tunnel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func handleCDPConnection(
	remoteConn net.Conn,
	localChromePort int,
	listenPort int,
	wsPath string,
	activeConns *atomic.Int64,
	totalConns *atomic.Int64,
	bytesXfer *atomic.Int64,
	onNotify func(),
	onWarn func(string),
) {
	defer remoteConn.Close()

	activeConns.Add(1)
	totalConns.Add(1)
	if onNotify != nil {
		onNotify()
	}
	defer func() {
		activeConns.Add(-1)
		if onNotify != nil {
			onNotify()
		}
	}()

	localTarget := fmt.Sprintf("127.0.0.1:%d", localChromePort)

	// Peek initial bytes briefly to distinguish HTTP/CDP discovery requests from raw/non-HTTP streams.
	_ = remoteConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReaderSize(remoteConn, 64*1024)
	peek, _ := br.Peek(8)
	_ = remoteConn.SetReadDeadline(time.Time{})

	if !looksLikeHTTP(peek) {
		localConn, err := net.DialTimeout("tcp", localTarget, 3*time.Second)
		if err != nil {
			if onWarn != nil {
				onWarn(fmt.Sprintf("Chrome local no respondió en %s: %v", localTarget, err))
			}
			return
		}
		defer localConn.Close()
		pipeBidirectional(localConn, br, remoteConn, bytesXfer)
		return
	}

	_ = remoteConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	req, err := http.ReadRequest(br)
	_ = remoteConn.SetReadDeadline(time.Time{})
	if err != nil {
		return
	}

	isUpgrade := strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
	path := req.URL.Path

	// For /json/version and /json/list HTTP discovery, rewrite Host and response WS URLs,
	// or synthesize /json/version when Chrome's inspect mode only exposes WebSocket.
	if !isUpgrade && (path == "/json/version" || path == "/json" || path == "/json/list") {
		defer req.Body.Close()
		if handleCDPDiscoveryHTTP(remoteConn, req, localChromePort, listenPort, wsPath, bytesXfer) {
			return
		}
	}

	localConn, err := net.DialTimeout("tcp", localTarget, 3*time.Second)
	if err != nil {
		if onWarn != nil {
			onWarn(fmt.Sprintf("Chrome local no respondió en %s: %v", localTarget, err))
		}
		return
	}
	defer localConn.Close()

	// Normalize Host header so Chrome accepts connections forwarded from another port (e.g. 9222 -> ephemeral).
	req.Host = localTarget
	if err := req.Write(localConn); err != nil {
		return
	}

	pipeBidirectional(localConn, br, remoteConn, bytesXfer)
}

func handleCDPDiscoveryHTTP(
	remoteConn net.Conn,
	req *http.Request,
	localChromePort int,
	listenPort int,
	wsPath string,
	bytesXfer *atomic.Int64,
) bool {
	localTarget := fmt.Sprintf("127.0.0.1:%d", localChromePort)
	localConn, err := net.DialTimeout("tcp", localTarget, 2*time.Second)
	if err == nil {
		defer localConn.Close()
		req.Host = localTarget
		req.Header.Set("Connection", "close")
		if werr := req.Write(localConn); werr == nil {
			resp, rerr := http.ReadResponse(bufio.NewReader(localConn), req)
			if rerr == nil {
				defer resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					body, berr := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
					if berr == nil {
						if listenPort != localChromePort {
							body = bytes.ReplaceAll(body, []byte(fmt.Sprintf("127.0.0.1:%d", localChromePort)), []byte(fmt.Sprintf("127.0.0.1:%d", listenPort)))
							body = bytes.ReplaceAll(body, []byte(fmt.Sprintf("localhost:%d", localChromePort)), []byte(fmt.Sprintf("127.0.0.1:%d", listenPort)))
						}
						resp.Body = io.NopCloser(bytes.NewReader(body))
						resp.ContentLength = int64(len(body))
						resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
						resp.Header.Set("Connection", "close")
						_ = resp.Write(remoteConn)
						bytesXfer.Add(int64(len(body)))
						return true
					}
				} else if req.URL.Path != "/json/version" {
					_ = resp.Write(remoteConn)
					return true
				}
			}
		}
	}

	// Fallback for /json/version when Chrome remote-debugging only exposes WS.
	if req.URL.Path == "/json/version" && wsPath != "" {
		payload, _ := json.Marshal(map[string]string{
			"Browser":              "Chrome/Chrome-Bridge-for-AI",
			"Protocol-Version":     "1.3",
			"webSocketDebuggerUrl": fmt.Sprintf("ws://127.0.0.1:%d%s", listenPort, wsPath),
		})
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        make(http.Header),
			Body:          io.NopCloser(bytes.NewReader(payload)),
			ContentLength: int64(len(payload)),
		}
		resp.Header.Set("Content-Type", "application/json; charset=UTF-8")
		resp.Header.Set("Content-Length", strconv.Itoa(len(payload)))
		resp.Header.Set("Connection", "close")
		_ = resp.Write(remoteConn)
		bytesXfer.Add(int64(len(payload)))
		return true
	}

	return false
}

func looksLikeHTTP(peek []byte) bool {
	s := string(peek)
	for _, prefix := range []string{"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS "} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func pipeBidirectional(localConn net.Conn, remoteReader io.Reader, remoteRaw net.Conn, bytesXfer *atomic.Int64) {
	var wg sync.WaitGroup
	wg.Add(2)

	copyStream := func(dst io.Writer, src io.Reader) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
				bytesXfer.Add(int64(n))
			}
			if rerr != nil {
				break
			}
		}
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
	}

	go copyStream(localConn, remoteReader)
	go copyStream(remoteRaw, localConn)
	wg.Wait()
}
