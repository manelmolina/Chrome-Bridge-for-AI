package tunnel

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"antigravity-chrome-tunnel/internal/chrome"
	"antigravity-chrome-tunnel/internal/config"
	"golang.org/x/crypto/ssh"
)

// State represents the current connection lifecycle state.
type State string

const (
	StateDisconnected  State = "disconnected"
	StateConnecting    State = "connecting"
	StateWaitingChrome State = "waiting_chrome"
	StateActive        State = "active"
	StateError         State = "error"
)

// ConnectRequest holds user-supplied parameters to open a tunnel.
type ConnectRequest struct {
	Host                  string `json:"host"`
	Port                  int    `json:"port"`
	Username              string `json:"username"`
	AuthType              string `json:"authType"` // "password" or "key"
	Password              string `json:"password"`
	PrivateKeyPath        string `json:"privateKeyPath"`
	PrivateKeyContent     string `json:"privateKeyContent"`
	KeyPassphrase         string `json:"keyPassphrase"`
	LocalForwardPorts     string `json:"localForwardPorts"`
	StandardCDPPort       int    `json:"standardCdpPort"`
	FallbackDebugPort     int    `json:"fallbackDebugPort"`
	CustomChromeDir       string `json:"customChromeDir"`
	RemoteChromeConfigDir string `json:"remoteChromeConfigDir"`
}

// LogEntry represents a timestamped log line for the UI.
type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"` // info, ok, warn, error
	Message string `json:"message"`
}

// StatusSnapshot is broadcast to the UI whenever state or counters change.
type StatusSnapshot struct {
	State              State            `json:"state"`
	Message            string           `json:"message"`
	Host               string           `json:"host"`
	Port               int              `json:"port"`
	Username           string           `json:"username"`
	LocalChrome        *chrome.Endpoint `json:"localChrome"`
	LocalChromeError   string           `json:"localChromeError,omitempty"`
	RemotePort         int              `json:"remotePort"`
	CDPEndpointPort    int              `json:"cdpEndpointPort"`
	RemoteFileSynced   bool             `json:"remoteFileSynced"`
	NodeOnVPS          string           `json:"nodeOnVps,omitempty"`
	DetectedTools      []string         `json:"detectedTools,omitempty"`
	ActiveConnections  int64            `json:"activeConnections"`
	TotalConnections   int64            `json:"totalConnections"`
	BytesTransferred   int64            `json:"bytesTransferred"`
	LocalForwards      []int            `json:"localForwards"`
	Logs               []LogEntry       `json:"logs"`
}

// Manager coordinates SSH connectivity, reverse Chrome DevTools forwarding, and state updates.
type Manager struct {
	store *config.Store

	mu               sync.Mutex
	state            State
	statusMsg        string
	req              ConnectRequest
	cancel           context.CancelFunc
	client           *ssh.Client
	revListener      net.Listener
	cdpListener      net.Listener
	localListeners   []net.Listener
	syncedEndpoint   *chrome.Endpoint
	remotePort       int
	cdpEndpointPort  int
	remoteFileSynced bool
	nodeOnVPS        string
	detectedTools    []string
	localForwards    []int

	localChrome    *chrome.Endpoint
	localChromeErr string

	activeConns atomic.Int64
	totalConns  atomic.Int64
	bytesXfer   atomic.Int64

	logsMu sync.Mutex
	logs   []LogEntry

	subMu       sync.Mutex
	subscribers map[chan StatusSnapshot]struct{}
}

// NewManager creates a tunnel Manager backed by the given config store.
func NewManager(store *config.Store) *Manager {
	m := &Manager{
		store:       store,
		state:       StateDisconnected,
		statusMsg:   "Listo para conectar",
		subscribers: make(map[chan StatusSnapshot]struct{}),
	}
	m.RefreshLocalChrome("", 9222)
	return m
}

// Subscribe returns a channel receiving status updates and a cancel function.
func (m *Manager) Subscribe() (<-chan StatusSnapshot, func()) {
	ch := make(chan StatusSnapshot, 16)
	m.subMu.Lock()
	m.subscribers[ch] = struct{}{}
	m.subMu.Unlock()

	ch <- m.Snapshot()

	return ch, func() {
		m.subMu.Lock()
		delete(m.subscribers, ch)
		m.subMu.Unlock()
		close(ch)
	}
}

func (m *Manager) notify() {
	snap := m.Snapshot()
	m.subMu.Lock()
	for ch := range m.subscribers {
		select {
		case ch <- snap:
		default:
			// Drop oldest if subscriber is slow.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snap:
			default:
			}
		}
	}
	m.subMu.Unlock()
}

func (m *Manager) addLog(level, msg string) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Level:   level,
		Message: msg,
	}
	m.logsMu.Lock()
	m.logs = append(m.logs, entry)
	if len(m.logs) > 200 {
		m.logs = m.logs[len(m.logs)-200:]
	}
	m.logsMu.Unlock()
	m.notify()
}

// Snapshot returns a thread-safe copy of the current tunnel status.
func (m *Manager) Snapshot() StatusSnapshot {
	m.mu.Lock()
	snap := StatusSnapshot{
		State:             m.state,
		Message:           m.statusMsg,
		Host:              m.req.Host,
		Port:              m.req.Port,
		Username:          m.req.Username,
		LocalChrome:       m.localChrome,
		LocalChromeError:  m.localChromeErr,
		RemotePort:        m.remotePort,
		CDPEndpointPort:   m.cdpEndpointPort,
		RemoteFileSynced:  m.remoteFileSynced,
		NodeOnVPS:         m.nodeOnVPS,
		DetectedTools:     append([]string(nil), m.detectedTools...),
		ActiveConnections: m.activeConns.Load(),
		TotalConnections:  m.totalConns.Load(),
		BytesTransferred:  m.bytesXfer.Load(),
		LocalForwards:     append([]int(nil), m.localForwards...),
	}
	m.mu.Unlock()

	m.logsMu.Lock()
	snap.Logs = append([]LogEntry(nil), m.logs...)
	m.logsMu.Unlock()
	return snap
}

// RefreshLocalChrome probes local Chrome debugging availability and updates status.
func (m *Manager) RefreshLocalChrome(customDir string, fallbackPort int) (*chrome.Endpoint, error) {
	if fallbackPort <= 0 {
		fallbackPort = 9222
	}
	ep, err := chrome.DiscoverLocalChrome(customDir, fallbackPort)
	m.mu.Lock()
	changed := (ep == nil) != (m.localChrome == nil) ||
		(ep != nil && m.localChrome != nil && (ep.Port != m.localChrome.Port || ep.WSPath != m.localChrome.WSPath))
	if err != nil {
		m.localChrome = nil
		m.localChromeErr = err.Error()
	} else {
		m.localChrome = ep
		m.localChromeErr = ""
	}
	m.mu.Unlock()
	if changed {
		m.notify()
	}
	return ep, err
}

// ParsePortList parses a comma/space-separated list of TCP port numbers.
func ParsePortList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '\t'
	})
	seen := make(map[int]struct{})
	var ports []int
	for _, f := range fields {
		p, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("puerto inválido en reenvío local: %q", f)
		}
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// Start establishes the SSH connection, optional local forwards, and Chrome reverse tunnel.
func (m *Manager) Start(req ConnectRequest) error {
	req.Host = strings.TrimSpace(req.Host)
	req.Username = strings.TrimSpace(req.Username)
	req.AuthType = strings.TrimSpace(strings.ToLower(req.AuthType))
	if req.AuthType == "" {
		if req.PrivateKeyPath != "" || req.PrivateKeyContent != "" {
			req.AuthType = "key"
		} else {
			req.AuthType = "password"
		}
	}
	if req.Host == "" {
		return errors.New("introduce la IP o dominio del servidor")
	}
	if req.Username == "" {
		return errors.New("introduce el usuario SSH")
	}
	authMethods, err := buildSSHAuthMethods(req)
	if err != nil {
		return err
	}
	if req.Port <= 0 || req.Port > 65535 {
		req.Port = 22
	}
	if req.StandardCDPPort <= 0 || req.StandardCDPPort > 65535 {
		req.StandardCDPPort = 9222
	}
	if req.FallbackDebugPort <= 0 || req.FallbackDebugPort > 65535 {
		req.FallbackDebugPort = 9222
	}
	localPorts, err := ParsePortList(req.LocalForwardPorts)
	if err != nil {
		return err
	}

	// Ensure any previous session is stopped cleanly first.
	m.Stop()

	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	m.state = StateConnecting
	m.statusMsg = fmt.Sprintf("Conectando a %s@%s:%d...", req.Username, req.Host, req.Port)
	m.req = req
	m.cancel = cancel
	m.localForwards = nil
	m.remotePort = 0
	m.cdpEndpointPort = 0
	m.remoteFileSynced = false
	m.nodeOnVPS = ""
	m.detectedTools = nil
	m.syncedEndpoint = nil
	m.totalConns.Store(0)
	m.bytesXfer.Store(0)
	m.activeConns.Store(0)
	m.mu.Unlock()

	authLabel := "contraseña"
	if req.AuthType == "key" {
		authLabel = "clave SSH"
	}
	m.addLog("info", fmt.Sprintf("Conectando por SSH (%s) a %s@%s:%d...", authLabel, req.Username, req.Host, req.Port))

	// Save non-sensitive profile for next launch (never passwords, passphrases, or key contents).
	if m.store != nil {
		_ = m.store.SaveProfile(config.Profile{
			Host:              req.Host,
			Port:              req.Port,
			Username:          req.Username,
			AuthType:          req.AuthType,
			PrivateKeyPath:    strings.TrimSpace(req.PrivateKeyPath),
			LocalForwardPorts: req.LocalForwardPorts,
			StandardCDPPort:   req.StandardCDPPort,
			FallbackDebugPort: req.FallbackDebugPort,
			CustomChromeDir:   req.CustomChromeDir,
		})
	}

	go m.runSession(ctx, req, authMethods, localPorts)
	return nil
}

func buildSSHAuthMethods(req ConnectRequest) ([]ssh.AuthMethod, error) {
	if req.AuthType == "key" {
		var keyBytes []byte
		if strings.TrimSpace(req.PrivateKeyContent) != "" {
			keyBytes = []byte(req.PrivateKeyContent)
		} else {
			keyPath := expandUserHome(strings.TrimSpace(req.PrivateKeyPath))
			if keyPath == "" {
				return nil, errors.New("selecciona un archivo o indica la ruta de la clave SSH")
			}
			data, err := os.ReadFile(keyPath)
			if err != nil {
				return nil, fmt.Errorf("no se pudo leer la clave SSH (%s): %w", keyPath, err)
			}
			keyBytes = data
		}

		var signer ssh.Signer
		var err error
		if req.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(keyBytes, []byte(req.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(keyBytes)
		}
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return nil, errors.New("la clave SSH está protegida con passphrase; introdúcela debajo")
			}
			return nil, fmt.Errorf("clave SSH inválida o passphrase incorrecta: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}

	if req.AuthType != "password" {
		return nil, fmt.Errorf("método de autenticación no soportado: %q", req.AuthType)
	}
	if req.Password == "" {
		return nil, errors.New("introduce la contraseña SSH")
	}
	return []ssh.AuthMethod{
		ssh.Password(req.Password),
		ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = req.Password
			}
			return answers, nil
		}),
	}, nil
}

func expandUserHome(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// DiscoverLocalSSHKeys returns paths of standard private SSH key files found in ~/.ssh.
func DiscoverLocalSSHKeys() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	sshDir := filepath.Join(home, ".ssh")
	names := []string{"id_ed25519", "id_ecdsa", "id_rsa", "id_dsa"}
	var found []string
	seen := make(map[string]struct{})
	for _, n := range names {
		p := filepath.Join(sshDir, n)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			found = append(found, p)
			seen[p] = struct{}{}
		}
	}
	entries, err := os.ReadDir(sshDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasSuffix(name, ".pub") ||
				strings.HasPrefix(name, "known_hosts") ||
				name == "authorized_keys" ||
				name == "authorized_keys2" ||
				name == "config" {
				continue
			}
			p := filepath.Join(sshDir, name)
			if _, ok := seen[p]; ok {
				continue
			}
			if _, err := os.Stat(p + ".pub"); err == nil {
				found = append(found, p)
				seen[p] = struct{}{}
			}
		}
	}
	return found
}

func (m *Manager) runSession(ctx context.Context, req ConnectRequest, authMethods []ssh.AuthMethod, localPorts []int) {
	sshCfg := &ssh.ClientConfig{
		User:            req.Username,
		Auth:            authMethods,
		HostKeyCallback: m.makeHostKeyCallback(req.Host, req.Port),
		Timeout:         12 * time.Second,
	}

	addr := net.JoinHostPort(req.Host, strconv.Itoa(req.Port))
	client, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		m.failSession(fmt.Sprintf("Error de conexión SSH: %v", err))
		return
	}

	// Check if cancelled while dialing.
	if ctx.Err() != nil {
		_ = client.Close()
		return
	}

	m.mu.Lock()
	m.client = client
	m.mu.Unlock()

	m.addLog("ok", fmt.Sprintf("Conectado por SSH a %s", addr))

	// Probe Node.js & AI CLI tools on remote server (informational).
	nodeVer, tools := probeRemoteTools(client)
	m.mu.Lock()
	m.nodeOnVPS = nodeVer
	m.detectedTools = tools
	m.mu.Unlock()
	if nodeVer != "" {
		m.addLog("info", fmt.Sprintf("Node.js detectado en el servidor: %s", nodeVer))
	} else {
		m.addLog("info", "Node.js no detectado en PATH del servidor (necesario para clientes MCP)")
	}
	if len(tools) > 0 {
		m.addLog("info", fmt.Sprintf("Herramientas detectadas en el servidor: %s", strings.Join(tools, ", ")))
	}

	// Start optional local port forwards (PC -> server).
	for _, p := range localPorts {
		if err := m.startLocalForward(ctx, client, p); err != nil {
			m.addLog("warn", fmt.Sprintf("No se pudo reenviar puerto local %d: %v", p, err))
		} else {
			m.mu.Lock()
			m.localForwards = append(m.localForwards, p)
			m.mu.Unlock()
			m.addLog("ok", fmt.Sprintf("Reenvío local activo: PC 127.0.0.1:%d → Servidor 127.0.0.1:%d", p, p))
		}
	}

	// Initial Chrome sync attempt.
	m.syncChromeState(ctx, client, req)

	// Start SSH keepalive + Chrome watcher loop.
	keepaliveTicker := time.NewTicker(15 * time.Second)
	chromeTicker := time.NewTicker(2 * time.Second)
	defer keepaliveTicker.Stop()
	defer chromeTicker.Stop()

	missedKeepalives := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-chromeTicker.C:
			m.syncChromeState(ctx, client, req)
		case <-keepaliveTicker.C:
			done := make(chan error, 1)
			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			select {
			case <-ctx.Done():
				return
			case err := <-done:
				if err != nil {
					missedKeepalives++
					if missedKeepalives >= 3 {
						m.failSession("Se perdió la conexión SSH con el servidor (keepalive sin respuesta)")
						return
					}
				} else {
					missedKeepalives = 0
				}
			case <-time.After(8 * time.Second):
				missedKeepalives++
				if missedKeepalives >= 3 {
					m.failSession("Tiempo de espera agotado en la conexión SSH con el servidor")
					return
				}
			}
		}
	}
}

func (m *Manager) syncChromeState(ctx context.Context, client *ssh.Client, req ConnectRequest) {
	ep, err := m.RefreshLocalChrome(req.CustomChromeDir, req.FallbackDebugPort)
	if err != nil {
		m.mu.Lock()
		hadSynced := m.syncedEndpoint != nil
		m.mu.Unlock()

		if hadSynced {
			m.addLog("warn", "Chrome local dejó de responder en depuración remota; esperando que vuelva a activarse...")
			m.teardownReverseForward(client, req.RemoteChromeConfigDir)
		}

		m.mu.Lock()
		if m.state != StateWaitingChrome {
			m.state = StateWaitingChrome
			m.statusMsg = "SSH conectado • Esperando Chrome local (activa chrome://inspect/#remote-debugging)"
		}
		m.mu.Unlock()
		m.notify()
		return
	}

	m.mu.Lock()
	same := m.syncedEndpoint != nil && m.syncedEndpoint.Port == ep.Port && m.syncedEndpoint.WSPath == ep.WSPath && m.revListener != nil
	m.mu.Unlock()
	if same {
		return
	}

	// Endpoint changed or newly detected: establish reverse tunnel.
	m.teardownReverseForward(client, req.RemoteChromeConfigDir)

	revLn, remotePort, err := openRemoteListener(client, ep.Port)
	if err != nil {
		m.addLog("error", fmt.Sprintf("No se pudo abrir el túnel inverso en el servidor: %v", err))
		m.mu.Lock()
		m.state = StateWaitingChrome
		m.statusMsg = fmt.Sprintf("Error al abrir puerto inverso en el servidor: %v", err)
		m.mu.Unlock()
		m.notify()
		return
	}

	if err := writeRemoteDevToolsActivePort(client, remotePort, ep.WSPath, req.RemoteChromeConfigDir); err != nil {
		_ = revLn.Close()
		m.addLog("error", fmt.Sprintf("No se pudo escribir DevToolsActivePort en el servidor: %v", err))
		m.mu.Lock()
		m.state = StateWaitingChrome
		m.statusMsg = fmt.Sprintf("Error escribiendo DevToolsActivePort en el servidor: %v", err)
		m.mu.Unlock()
		m.notify()
		return
	}

	var cdpLn net.Listener
	cdpPort := remotePort
	stdPortBusy := false
	if req.StandardCDPPort > 0 {
		if remotePort == req.StandardCDPPort {
			cdpPort = remotePort
		} else if ep.Port == req.StandardCDPPort && remotePort != ep.Port {
			stdPortBusy = true
		} else {
			if l, err := client.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", req.StandardCDPPort)); err == nil {
				cdpLn = l
				cdpPort = req.StandardCDPPort
			} else {
				stdPortBusy = true
			}
		}
	}

	m.mu.Lock()
	m.revListener = revLn
	m.cdpListener = cdpLn
	m.syncedEndpoint = ep
	m.remotePort = remotePort
	m.cdpEndpointPort = cdpPort
	m.remoteFileSynced = true
	m.state = StateActive
	m.statusMsg = fmt.Sprintf("Túnel activo • Servidor :%d / CDP :%d → Chrome local :%d", remotePort, cdpPort, ep.Port)
	m.mu.Unlock()

	if remotePort != ep.Port && ep.Port != req.StandardCDPPort {
		m.addLog("warn", fmt.Sprintf("Puerto %d ocupado en el servidor; usando puerto remoto %d", ep.Port, remotePort))
	}
	if stdPortBusy {
		m.addLog("warn", fmt.Sprintf("El puerto %d está ocupado en el servidor; endpoint CDP alternativo en http://127.0.0.1:%d (o usa --autoConnect)", req.StandardCDPPort, remotePort))
	}
	m.addLog("ok", fmt.Sprintf("Túnel Chrome activo: Servidor 127.0.0.1:%d → PC 127.0.0.1:%d (%s)", remotePort, ep.Port, ep.SourceType))
	if cdpLn != nil || remotePort == req.StandardCDPPort {
		m.addLog("ok", fmt.Sprintf("Endpoint CDP estándar activo en el servidor: http://127.0.0.1:%d", cdpPort))
	}
	m.addLog("ok", "DevToolsActivePort sincronizado en el servidor (compatible con --autoConnect / Antigravity / MCP).")
	m.notify()

	go m.acceptReverseLoop(ctx, revLn, ep.Port, remotePort, ep.WSPath)
	if cdpLn != nil {
		go m.acceptReverseLoop(ctx, cdpLn, ep.Port, cdpPort, ep.WSPath)
	}
}

func openRemoteListener(client *ssh.Client, preferredPort int) (net.Listener, int, error) {
	ln, err := client.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	if err == nil {
		return ln, ln.Addr().(*net.TCPAddr).Port, nil
	}
	// Fallback to any free port on 127.0.0.1 in the VPS.
	ln, err2 := client.Listen("tcp", "127.0.0.1:0")
	if err2 != nil {
		return nil, 0, err
	}
	return ln, ln.Addr().(*net.TCPAddr).Port, nil
}

func (m *Manager) acceptReverseLoop(ctx context.Context, revLn net.Listener, localChromePort, listenPort int, wsPath string) {
	for {
		remoteConn, err := revLn.Accept()
		if err != nil {
			return
		}
		if ctx.Err() != nil {
			_ = remoteConn.Close()
			return
		}
		go handleCDPConnection(
			remoteConn,
			localChromePort,
			listenPort,
			wsPath,
			&m.activeConns,
			&m.totalConns,
			&m.bytesXfer,
			m.notify,
			func(msg string) { m.addLog("warn", msg) },
		)
	}
}

func (m *Manager) startLocalForward(ctx context.Context, client *ssh.Client, port int) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.localListeners = append(m.localListeners, ln)
	m.mu.Unlock()

	go func() {
		target := fmt.Sprintf("127.0.0.1:%d", port)
		for {
			localConn, err := ln.Accept()
			if err != nil {
				return
			}
			if ctx.Err() != nil {
				_ = localConn.Close()
				return
			}
			go func(lc net.Conn) {
				defer lc.Close()
				rc, err := client.Dial("tcp", target)
				if err != nil {
					return
				}
				defer rc.Close()
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, _ = io.Copy(rc, lc) }()
				go func() { defer wg.Done(); _, _ = io.Copy(lc, rc) }()
				wg.Wait()
			}(localConn)
		}
	}()
	return nil
}

func (m *Manager) teardownReverseForward(client *ssh.Client, remoteConfigDir string) {
	m.mu.Lock()
	revLn := m.revListener
	m.revListener = nil
	cdpLn := m.cdpListener
	m.cdpListener = nil
	wasSynced := m.remoteFileSynced
	m.remoteFileSynced = false
	m.remotePort = 0
	m.cdpEndpointPort = 0
	m.syncedEndpoint = nil
	m.mu.Unlock()

	if revLn != nil {
		_ = revLn.Close()
	}
	if cdpLn != nil {
		_ = cdpLn.Close()
	}
	if wasSynced && client != nil {
		_ = removeRemoteDevToolsActivePort(client, remoteConfigDir)
	}
}

func (m *Manager) failSession(msg string) {
	m.cleanupResources(false)
	m.mu.Lock()
	m.state = StateError
	m.statusMsg = msg
	m.mu.Unlock()
	m.addLog("error", msg)
}

// Stop disconnects the active SSH session and cleans up remote DevToolsActivePort.
func (m *Manager) Stop() {
	m.cleanupResources(true)
	m.mu.Lock()
	wasConnected := m.state != StateDisconnected
	m.state = StateDisconnected
	m.statusMsg = "Desconectado"
	m.mu.Unlock()
	if wasConnected {
		m.addLog("info", "Túnel detenido y archivo remoto DevToolsActivePort limpiado.")
	} else {
		m.notify()
	}
}

func (m *Manager) cleanupResources(removeRemote bool) {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	client := m.client
	m.client = nil
	revLn := m.revListener
	m.revListener = nil
	cdpLn := m.cdpListener
	m.cdpListener = nil
	localLns := m.localListeners
	m.localListeners = nil
	wasSynced := m.remoteFileSynced
	remoteDir := m.req.RemoteChromeConfigDir
	m.remoteFileSynced = false
	m.remotePort = 0
	m.cdpEndpointPort = 0
	m.syncedEndpoint = nil
	m.localForwards = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if revLn != nil {
		_ = revLn.Close()
	}
	if cdpLn != nil {
		_ = cdpLn.Close()
	}
	for _, ln := range localLns {
		_ = ln.Close()
	}
	if removeRemote && wasSynced && client != nil {
		_ = removeRemoteDevToolsActivePort(client, remoteDir)
	}
	if client != nil {
		_ = client.Close()
	}
}

func (m *Manager) makeHostKeyCallback(host string, port int) ssh.HostKeyCallback {
	id := fmt.Sprintf("%s:%d", host, port)
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		fp := ssh.FingerprintSHA256(key)
		if m.store == nil {
			return nil
		}
		known := m.store.LoadKnownHosts()
		if existing, ok := known[id]; ok {
			if existing != fp {
				return fmt.Errorf("la huella SSH de %s cambió (esperada %s, recibida %s)", id, existing, fp)
			}
			return nil
		}
		if err := m.store.SaveKnownHost(id, fp); err != nil {
			m.addLog("warn", fmt.Sprintf("No se pudo guardar huella SSH: %v", err))
		} else {
			m.addLog("info", fmt.Sprintf("Huella SSH confiada (%s): %s", id, fp))
		}
		return nil
	}
}

func probeRemoteTools(client *ssh.Client) (string, []string) {
	sess, err := client.NewSession()
	if err != nil {
		return "", nil
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(`sh -c 'node_v=$(command -v node >/dev/null 2>&1 && node --version || true); echo "NODE:$node_v"; for t in agy codex claude npx; do command -v "$t" >/dev/null 2>&1 && echo "TOOL:$t"; done'`)
	if err != nil {
		return "", nil
	}
	var nodeVer string
	var tools []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "NODE:") {
			nodeVer = strings.TrimPrefix(line, "NODE:")
		} else if strings.HasPrefix(line, "TOOL:") {
			tools = append(tools, strings.TrimPrefix(line, "TOOL:"))
		}
	}
	return nodeVer, tools
}

func writeRemoteDevToolsActivePort(client *ssh.Client, port int, wsPath, customConfigDir string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	var cmd string
	if strings.TrimSpace(customConfigDir) != "" {
		cmd = fmt.Sprintf(
			`sh -c 'dir="$1"; mkdir -p "$dir" && tmp="$dir/.DevToolsActivePort.tmp.$$" && printf "%%s\n%%s\n" "$2" "$3" > "$tmp" && mv "$tmp" "$dir/DevToolsActivePort"' _ %s %s %s`,
			shellQuote(customConfigDir),
			shellQuote(strconv.Itoa(port)),
			shellQuote(wsPath),
		)
	} else {
		cmd = fmt.Sprintf(
			`sh -c 'dir="${CHROME_CONFIG_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}}/google-chrome"; mkdir -p "$dir" && tmp="$dir/.DevToolsActivePort.tmp.$$" && printf "%%s\n%%s\n" "$1" "$2" > "$tmp" && mv "$tmp" "$dir/DevToolsActivePort"' _ %s %s`,
			shellQuote(strconv.Itoa(port)),
			shellQuote(wsPath),
		)
	}
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func removeRemoteDevToolsActivePort(client *ssh.Client, customConfigDir string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()

	var cmd string
	if strings.TrimSpace(customConfigDir) != "" {
		cmd = fmt.Sprintf(`sh -c 'rm -f "$1/DevToolsActivePort"' _ %s`, shellQuote(customConfigDir))
	} else {
		cmd = `sh -c 'rm -f "${CHROME_CONFIG_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}}/google-chrome/DevToolsActivePort"'`
	}
	return sess.Run(cmd)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RandomToken returns a random hex string for authenticating local UI requests.
func RandomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	const hexChars = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[i*2] = hexChars[v>>4]
		out[i*2+1] = hexChars[v&0x0f]
	}
	return string(out)
}
