package ui

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"antigravity-chrome-tunnel/internal/chrome"
	"antigravity-chrome-tunnel/internal/config"
	"antigravity-chrome-tunnel/internal/tunnel"
)

//go:embed assets/*
var embeddedAssets embed.FS

// Server serves the local desktop web UI and JSON/SSE API.
type Server struct {
	mgr       *tunnel.Manager
	store     *config.Store
	token     string
	httpSrv   *http.Server
	listener  net.Listener
	onClose   func()
	hadClient atomic.Bool

	clientsMu   sync.Mutex
	sseClients  int
	closeTimer  *time.Timer
}

// NewServer creates and binds the local UI HTTP server on 127.0.0.1:0.
func NewServer(mgr *tunnel.Manager, store *config.Store, onClose func()) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	s := &Server{
		mgr:      mgr,
		store:    store,
		token:    tunnel.RandomToken(),
		listener: ln,
		onClose:  onClose,
	}

	assetsFS, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	fileSrv := http.FileServer(http.FS(assetsFS))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/profile", s.withAuth(s.handleProfile))
	mux.HandleFunc("/api/ssh-keys", s.withAuth(s.handleSSHKeys))
	mux.HandleFunc("/api/status", s.withAuth(s.handleStatus))
	mux.HandleFunc("/api/events", s.withAuth(s.handleEvents))
	mux.HandleFunc("/api/connect", s.withAuth(s.handleConnect))
	mux.HandleFunc("/api/disconnect", s.withAuth(s.handleDisconnect))
	mux.HandleFunc("/api/chrome/refresh", s.withAuth(s.handleChromeRefresh))
	mux.HandleFunc("/api/chrome/launch-debug", s.withAuth(s.handleLaunchDebug))
	mux.HandleFunc("/api/window-closed", s.withAuth(s.handleWindowClosed))
	mux.Handle("/", s.withRootTokenCheck(fileSrv))

	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// URL returns the authenticated local URL to open in the desktop window.
func (s *Server) URL() string {
	return "http://" + s.listener.Addr().String() + "/?token=" + s.token
}

// Start begins serving HTTP requests in the background.
func (s *Server) Start() {
	go func() {
		_ = s.httpSrv.Serve(s.listener)
	}()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) withRootTokenCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			if r.URL.Query().Get("token") != s.token {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-App-Token")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}
		if tok != s.token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var prof config.Profile
	if s.store != nil {
		prof = s.store.LoadProfile()
	} else {
		prof = config.Profile{Port: 22, Username: "root", FallbackDebugPort: 9222}
	}
	writeJSON(w, http.StatusOK, prof)
}

func (s *Server) handleSSHKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	keys := tunnel.DiscoverLocalSSHKeys()
	if keys == nil {
		keys = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.mgr.Snapshot())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	s.hadClient.Store(true)
	s.trackSSEClient(1)
	defer s.trackSSEClient(-1)

	ch, unsub := s.mgr.Subscribe()
	defer unsub()

	// Periodically refresh local Chrome detection while UI is open.
	refreshTicker := time.NewTicker(2 * time.Second)
	defer refreshTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-refreshTicker.C:
			snap := s.mgr.Snapshot()
			if snap.State == tunnel.StateDisconnected || snap.State == tunnel.StateError {
				_, _ = s.mgr.RefreshLocalChrome("", 9222)
			}
		case snap, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(data)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) trackSSEClient(delta int) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	s.sseClients += delta
	if s.sseClients > 0 {
		if s.closeTimer != nil {
			s.closeTimer.Stop()
			s.closeTimer = nil
		}
		return
	}
	if s.hadClient.Load() && s.sseClients <= 0 && s.onClose != nil {
		if s.closeTimer != nil {
			s.closeTimer.Stop()
		}
		s.closeTimer = time.AfterFunc(3*time.Second, func() {
			s.clientsMu.Lock()
			count := s.sseClients
			s.clientsMu.Unlock()
			if count <= 0 {
				s.onClose()
			}
		})
	}
}

func (s *Server) handleWindowClosed(w http.ResponseWriter, r *http.Request) {
	s.clientsMu.Lock()
	if s.closeTimer != nil {
		s.closeTimer.Stop()
	}
	if s.onClose != nil {
		s.closeTimer = time.AfterFunc(1200*time.Millisecond, func() {
			s.clientsMu.Lock()
			count := s.sseClients
			s.clientsMu.Unlock()
			if count <= 0 {
				s.onClose()
			}
		})
	}
	s.clientsMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tunnel.ConnectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "JSON inválido"})
		return
	}
	if err := s.mgr.Start(req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mgr.Stop()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChromeRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	customDir := strings.TrimSpace(r.URL.Query().Get("customDir"))
	ep, err := s.mgr.RefreshLocalChrome(customDir, 9222)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": true, "endpoint": ep})
}

func (s *Server) handleLaunchDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := chrome.LaunchIsolatedChromeDebug(9222); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	go func() {
		time.Sleep(1200 * time.Millisecond)
		_, _ = s.mgr.RefreshLocalChrome("", 9222)
	}()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
