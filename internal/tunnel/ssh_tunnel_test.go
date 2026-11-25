package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"antigravity-chrome-tunnel/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestEndToEndReverseTunnelAndDevToolsSync(t *testing.T) {
	// 1. Start a local TCP echo server simulating Chrome DevTools listening locally.
	chromeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer chromeLn.Close()
	chromePort := chromeLn.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			c, err := chromeLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	// Write local DevToolsActivePort file.
	localChromeDir := t.TempDir()
	localPortFile := filepath.Join(localChromeDir, "DevToolsActivePort")
	if err := os.WriteFile(localPortFile, []byte(fmt.Sprintf("%d\n/devtools/browser/test-session\n", chromePort)), 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. Start an in-process mock SSH VPS server.
	remoteChromeDir := t.TempDir()
	mockVPS := startMockSSHServer(t, "testuser", "secret123", remoteChromeDir)
	defer mockVPS.Close()

	// 3. Reserve a separate free port to test the standard CDP endpoint (e.g. 9222).
	probeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stdCDPPort := probeLn.Addr().(*net.TCPAddr).Port
	_ = probeLn.Close()

	// 4. Start tunnel Manager.
	store, err := config.NewStoreInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store)
	defer mgr.Stop()

	err = mgr.Start(ConnectRequest{
		Host:                  mockVPS.Host,
		Port:                  mockVPS.Port,
		Username:              "testuser",
		Password:              "secret123",
		StandardCDPPort:       stdCDPPort,
		CustomChromeDir:       localChromeDir,
		RemoteChromeConfigDir: remoteChromeDir,
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Wait until StateActive and remote DevToolsActivePort is written.
	deadline := time.Now().Add(8 * time.Second)
	var snap StatusSnapshot
	for time.Now().Before(deadline) {
		snap = mgr.Snapshot()
		if snap.State == StateActive && snap.RemoteFileSynced {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap.State != StateActive {
		t.Fatalf("expected active state, got %s (%s)", snap.State, snap.Message)
	}

	remoteFile := filepath.Join(remoteChromeDir, "DevToolsActivePort")
	rawRemote, err := os.ReadFile(remoteFile)
	if err != nil {
		t.Fatalf("expected remote DevToolsActivePort file, got error: %v", err)
	}
	expectedContent := fmt.Sprintf("%d\n/devtools/browser/test-session\n", snap.RemotePort)
	if string(rawRemote) != expectedContent {
		t.Fatalf("remote DevToolsActivePort mismatch:\ngot:\n%q\nwant:\n%q", string(rawRemote), expectedContent)
	}

	// 4. Simulate Antigravity on the VPS connecting to 127.0.0.1:<remotePort>.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", snap.RemotePort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial remote forwarded port failed: %v", err)
	}
	defer conn.Close()

	payload := "RAW-TUNNEL-ECHO-PAYLOAD\n"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("tunnel echo mismatch: got %q, want %q", string(buf), payload)
	}
	_ = conn.Close()

	// 5. Simulate Codex / Claude Code / Playwright connecting to the standard CDP port.
	cdpConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", snap.CDPEndpointPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial standard CDP port failed: %v", err)
	}
	defer cdpConn.Close()
	if _, err := cdpConn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	buf2 := make([]byte, len(payload))
	if _, err := io.ReadFull(cdpConn, buf2); err != nil {
		t.Fatal(err)
	}
	if string(buf2) != payload {
		t.Fatalf("standard CDP tunnel echo mismatch: got %q, want %q", string(buf2), payload)
	}
	_ = cdpConn.Close()

	// 6. Stop tunnel and verify remote DevToolsActivePort was cleaned up.
	mgr.Stop()
	if _, err := os.Stat(remoteFile); !os.IsNotExist(err) {
		t.Fatalf("expected remote DevToolsActivePort to be removed on Stop, stat err=%v", err)
	}
}

func TestBusyStandardCDPPortLogsWarning(t *testing.T) {
	chromeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer chromeLn.Close()
	chromePort := chromeLn.Addr().(*net.TCPAddr).Port

	localChromeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localChromeDir, "DevToolsActivePort"), []byte(fmt.Sprintf("%d\n/devtools/browser/busy-test\n", chromePort)), 0o600); err != nil {
		t.Fatal(err)
	}

	// Keep a port busy so StandardCDPPort cannot bind it on the mock VPS.
	busyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busyLn.Close()
	busyPort := busyLn.Addr().(*net.TCPAddr).Port

	remoteChromeDir := t.TempDir()
	mockVPS := startMockSSHServer(t, "testuser", "secret123", remoteChromeDir)
	defer mockVPS.Close()

	store, err := config.NewStoreInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store)
	defer mgr.Stop()

	if err := mgr.Start(ConnectRequest{
		Host:                  mockVPS.Host,
		Port:                  mockVPS.Port,
		Username:              "testuser",
		Password:              "secret123",
		StandardCDPPort:       busyPort,
		CustomChromeDir:       localChromeDir,
		RemoteChromeConfigDir: remoteChromeDir,
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var snap StatusSnapshot
	for time.Now().Before(deadline) {
		snap = mgr.Snapshot()
		if snap.State == StateActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap.State != StateActive {
		t.Fatalf("expected active state, got %s (%s)", snap.State, snap.Message)
	}

	foundWarn := false
	wantSubstr := fmt.Sprintf("El puerto %d está ocupado en el servidor", busyPort)
	for _, entry := range snap.Logs {
		if entry.Level == "warn" && strings.Contains(entry.Message, wantSubstr) {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Fatalf("expected warning log containing %q, got logs: %+v", wantSubstr, snap.Logs)
	}
}

func TestEndToEndSSHKeyAuth(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(pemBlock), 0o600); err != nil {
		t.Fatal(err)
	}

	encBlock, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("keypass"))
	if err != nil {
		t.Fatal(err)
	}
	encKeyPEM := string(pem.EncodeToMemory(encBlock))

	chromeLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer chromeLn.Close()
	chromePort := chromeLn.Addr().(*net.TCPAddr).Port

	localChromeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localChromeDir, "DevToolsActivePort"), []byte(fmt.Sprintf("%d\n/devtools/browser/key-test\n", chromePort)), 0o600); err != nil {
		t.Fatal(err)
	}

	remoteChromeDir := t.TempDir()
	mockVPS := startMockSSHServerWithAuth(t, "keyuser", "", sshPub, remoteChromeDir)
	defer mockVPS.Close()

	store, err := config.NewStoreInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(store)
	defer mgr.Stop()

	// 1. Missing passphrase for encrypted key should fail fast in Start.
	if err := mgr.Start(ConnectRequest{
		Host:              mockVPS.Host,
		Port:              mockVPS.Port,
		Username:          "keyuser",
		AuthType:          "key",
		PrivateKeyContent: encKeyPEM,
	}); err == nil {
		t.Fatal("expected error for encrypted key without passphrase")
	}

	// 2. Connect using unencrypted key file path.
	if err := mgr.Start(ConnectRequest{
		Host:                  mockVPS.Host,
		Port:                  mockVPS.Port,
		Username:              "keyuser",
		AuthType:              "key",
		PrivateKeyPath:        keyFile,
		CustomChromeDir:       localChromeDir,
		RemoteChromeConfigDir: remoteChromeDir,
	}); err != nil {
		t.Fatalf("Start with key file failed: %v", err)
	}

	deadline := time.Now().Add(8 * time.Second)
	var snap StatusSnapshot
	for time.Now().Before(deadline) {
		snap = mgr.Snapshot()
		if snap.State == StateActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap.State != StateActive {
		t.Fatalf("expected active state with SSH key, got %s (%s)", snap.State, snap.Message)
	}
	mgr.Stop()

	// 3. Connect using passphrase-protected key content.
	if err := mgr.Start(ConnectRequest{
		Host:                  mockVPS.Host,
		Port:                  mockVPS.Port,
		Username:              "keyuser",
		AuthType:              "key",
		PrivateKeyContent:     encKeyPEM,
		KeyPassphrase:         "keypass",
		CustomChromeDir:       localChromeDir,
		RemoteChromeConfigDir: remoteChromeDir,
	}); err != nil {
		t.Fatalf("Start with encrypted key content failed: %v", err)
	}

	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snap = mgr.Snapshot()
		if snap.State == StateActive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if snap.State != StateActive {
		t.Fatalf("expected active state with encrypted SSH key, got %s (%s)", snap.State, snap.Message)
	}
}

type mockSSHServer struct {
	Host string
	Port int
	ln   net.Listener
}

func (s *mockSSHServer) Close() {
	_ = s.ln.Close()
}

func startMockSSHServer(t *testing.T, expectedUser, expectedPass, remoteChromeDir string) *mockSSHServer {
	return startMockSSHServerWithAuth(t, expectedUser, expectedPass, nil, remoteChromeDir)
}

func startMockSSHServerWithAuth(t *testing.T, expectedUser, expectedPass string, expectedPub ssh.PublicKey, remoteChromeDir string) *mockSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &ssh.ServerConfig{}
	if expectedPass != "" {
		cfg.PasswordCallback = func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == expectedUser && string(pass) == expectedPass {
				return nil, nil
			}
			return nil, fmt.Errorf("rejected")
		}
	}
	if expectedPub != nil {
		cfg.PublicKeyCallback = func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == expectedUser && bytes.Equal(key.Marshal(), expectedPub.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("rejected key")
		}
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)

	go func() {
		for {
			tcpConn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleMockSSHConn(tcpConn, cfg, remoteChromeDir)
		}
	}()

	return &mockSSHServer{Host: "127.0.0.1", Port: addr.Port, ln: ln}
}

func handleMockSSHConn(tcpConn net.Conn, cfg *ssh.ServerConfig, remoteChromeDir string) {
	sshConn, chans, reqs, err := ssh.NewServerConn(tcpConn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()

	var fwdMu sync.Mutex
	var fwdListeners []net.Listener
	defer func() {
		fwdMu.Lock()
		for _, l := range fwdListeners {
			_ = l.Close()
		}
		fwdMu.Unlock()
	}()

	go func() {
		for req := range reqs {
			switch req.Type {
			case "tcpip-forward":
				var payload struct {
					Addr string
					Port uint32
				}
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", payload.Port))
				if err != nil {
					_ = req.Reply(false, nil)
					continue
				}
				boundPort := uint32(ln.Addr().(*net.TCPAddr).Port)
				fwdMu.Lock()
				fwdListeners = append(fwdListeners, ln)
				fwdMu.Unlock()

				resp := make([]byte, 4)
				binary.BigEndian.PutUint32(resp, boundPort)
				_ = req.Reply(true, resp)

				go func(l net.Listener, origAddr string, origPort uint32) {
					for {
						c, err := l.Accept()
						if err != nil {
							return
						}
						go func(conn net.Conn) {
							defer conn.Close()
							srcAddr := conn.RemoteAddr().(*net.TCPAddr)
							openPayload := ssh.Marshal(struct {
								DestAddr   string
								DestPort   uint32
								OriginAddr string
								OriginPort uint32
							}{
								DestAddr:   origAddr,
								DestPort:   origPort,
								OriginAddr: srcAddr.IP.String(),
								OriginPort: uint32(srcAddr.Port),
							})
							ch, chReqs, err := sshConn.OpenChannel("forwarded-tcpip", openPayload)
							if err != nil {
								return
							}
							defer ch.Close()
							go ssh.DiscardRequests(chReqs)
							var wg sync.WaitGroup
							wg.Add(2)
							go func() { defer wg.Done(); _, _ = io.Copy(ch, conn) }()
							go func() { defer wg.Done(); _, _ = io.Copy(conn, ch) }()
							wg.Wait()
						}(c)
					}
				}(ln, payload.Addr, boundPort)
			case "keepalive@openssh.com":
				_ = req.Reply(true, nil)
			default:
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}
	}()

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "unknown")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go func(channel ssh.Channel, in <-chan *ssh.Request) {
			defer channel.Close()
			for r := range in {
				if r.Type != "exec" {
					if r.WantReply {
						_ = r.Reply(false, nil)
					}
					continue
				}
				var execReq struct{ Command string }
				_ = ssh.Unmarshal(r.Payload, &execReq)
				_ = r.Reply(true, nil)

				exitCode := uint32(0)
				cmd := execReq.Command
				if strings.Contains(cmd, "DevToolsActivePort") && strings.Contains(cmd, "mv ") {
					// Extract arguments from our test command or write file in remoteChromeDir.
					// Command format ends with: _ '<dir>' '<port>' '<wsPath>'
					parts := extractSingleQuotedArgs(cmd)
					if len(parts) >= 3 {
						dir := parts[len(parts)-3]
						port := parts[len(parts)-2]
						ws := parts[len(parts)-1]
						if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(port+"\n"+ws+"\n"), 0o600); err != nil {
							exitCode = 1
						}
					}
				} else if strings.Contains(cmd, "rm -f") {
					parts := extractSingleQuotedArgs(cmd)
					if len(parts) >= 1 {
						_ = os.Remove(filepath.Join(parts[len(parts)-1], "DevToolsActivePort"))
					}
				}
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, exitCode)
				_, _ = channel.SendRequest("exit-status", false, status)
				return
			}
		}(ch, chReqs)
	}
}

func extractSingleQuotedArgs(cmd string) []string {
	var out []string
	idx := strings.LastIndex(cmd, "' _ '")
	if idx == -1 {
		return out
	}
	rest := cmd[idx+4:]
	for len(rest) > 0 {
		if rest[0] != '\'' {
			rest = rest[1:]
			continue
		}
		rest = rest[1:]
		end := strings.IndexByte(rest, '\'')
		if end == -1 {
			break
		}
		out = append(out, rest[:end])
		rest = rest[end+1:]
	}
	return out
}
