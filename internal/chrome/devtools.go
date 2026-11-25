package chrome

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Endpoint describes an active local Chrome DevTools debugging endpoint.
type Endpoint struct {
	Port       int    `json:"port"`
	WSPath     string `json:"wsPath"`
	SourcePath string `json:"sourcePath"`
	SourceType string `json:"sourceType"` // "DevToolsActivePort" or "json/version"
}

var validWSPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// ParseDevToolsActivePort parses and validates the contents of Chrome's DevToolsActivePort file.
func ParseDevToolsActivePort(content string) (int, string, error) {
	rawLines := strings.Split(content, "\n")
	lines := make([]string, 0, 2)
	for _, l := range rawLines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) < 2 {
		return 0, "", errors.New("DevToolsActivePort debe contener puerto y ruta WebSocket")
	}
	port, err := strconv.Atoi(lines[0])
	if err != nil || port < 1 || port > 65535 {
		return 0, "", fmt.Errorf("puerto inválido en DevToolsActivePort: %q", lines[0])
	}
	wsPath := lines[1]
	if !validWSPathRe.MatchString(wsPath) {
		return 0, "", fmt.Errorf("ruta WebSocket inválida en DevToolsActivePort: %q", wsPath)
	}
	return port, wsPath, nil
}

// DefaultPortFileCandidates returns ordered candidate paths for DevToolsActivePort on the current OS.
func DefaultPortFileCandidates() []string {
	home, _ := os.UserHomeDir()
	var dirs []string

	if runtime.GOOS == "windows" {
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" && home != "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		if localAppData != "" {
			dirs = append(dirs,
				filepath.Join(localAppData, "Google", "Chrome", "User Data"),
				filepath.Join(localAppData, "Google", "Chrome Beta", "User Data"),
				filepath.Join(localAppData, "Google", "Chrome SxS", "User Data"),
				filepath.Join(localAppData, "Google", "Chrome Dev", "User Data"),
				filepath.Join(localAppData, "Chromium", "User Data"),
				filepath.Join(localAppData, "Microsoft", "Edge", "User Data"),
				filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "User Data"),
			)
		}
	} else if runtime.GOOS == "darwin" {
		if home != "" {
			appSupport := filepath.Join(home, "Library", "Application Support")
			dirs = append(dirs,
				filepath.Join(appSupport, "Google", "Chrome"),
				filepath.Join(appSupport, "Google", "Chrome Beta"),
				filepath.Join(appSupport, "Google", "Chrome Canary"),
				filepath.Join(appSupport, "Google", "Chrome Dev"),
				filepath.Join(appSupport, "Chromium"),
				filepath.Join(appSupport, "Microsoft Edge"),
				filepath.Join(appSupport, "BraveSoftware", "Brave-Browser"),
			)
		}
	} else {
		if v := os.Getenv("CHROME_CONFIG_HOME"); v != "" {
			dirs = append(dirs, filepath.Join(v, "google-chrome"))
		}
		if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
			dirs = append(dirs, filepath.Join(v, "google-chrome"))
		}
		if home != "" {
			dirs = append(dirs,
				filepath.Join(home, ".config", "google-chrome"),
				filepath.Join(home, ".config", "google-chrome-beta"),
				filepath.Join(home, ".config", "google-chrome-unstable"),
				filepath.Join(home, ".config", "chromium"),
				filepath.Join(home, "snap", "chromium", "common", "chromium"),
				filepath.Join(home, ".var", "app", "com.google.Chrome", "config", "google-chrome"),
				filepath.Join(home, ".config", "microsoft-edge"),
				filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser"),
			)
		}
	}

	seen := make(map[string]struct{}, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		p := filepath.Join(d, "DevToolsActivePort")
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// DiscoverLocalChrome locates an active local Chrome remote debugging endpoint.
func DiscoverLocalChrome(customDir string, fallbackPort int) (*Endpoint, error) {
	var candidates []string
	if customDir = strings.TrimSpace(customDir); customDir != "" {
		if strings.HasSuffix(customDir, "DevToolsActivePort") {
			candidates = []string{customDir}
		} else {
			candidates = []string{filepath.Join(customDir, "DevToolsActivePort")}
		}
	} else {
		candidates = DefaultPortFileCandidates()
	}

	var staleErr error
	for _, portFile := range candidates {
		data, err := os.ReadFile(portFile)
		if err != nil {
			continue
		}
		port, wsPath, err := ParseDevToolsActivePort(string(data))
		if err != nil {
			staleErr = err
			continue
		}
		if !isLocalPortReachable(port) {
			staleErr = fmt.Errorf("DevToolsActivePort en %s apunta al puerto %d pero Chrome no responde", portFile, port)
			continue
		}
		return &Endpoint{
			Port:       port,
			WSPath:     wsPath,
			SourcePath: portFile,
			SourceType: "DevToolsActivePort",
		}, nil
	}

	if fallbackPort > 0 && fallbackPort <= 65535 {
		if ep, err := probeHTTPVersion(fallbackPort); err == nil {
			return ep, nil
		}
	}

	if staleErr != nil {
		return nil, staleErr
	}
	return nil, errors.New("no se detectó Chrome con depuración remota activa")
}

func isLocalPortReachable(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 350*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func probeHTTPVersion(port int) (*Endpoint, error) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d desde /json/version", resp.StatusCode)
	}
	var payload struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.WebSocketDebuggerURL == "" {
		return nil, errors.New("webSocketDebuggerUrl vacío")
	}
	u, err := url.Parse(payload.WebSocketDebuggerURL)
	if err != nil {
		return nil, err
	}
	if !validWSPathRe.MatchString(u.Path) {
		return nil, fmt.Errorf("ruta WebSocket inválida: %q", u.Path)
	}
	p := port
	if u.Port() != "" {
		if parsed, err := strconv.Atoi(u.Port()); err == nil && parsed > 0 && parsed <= 65535 {
			p = parsed
		}
	}
	return &Endpoint{
		Port:       p,
		WSPath:     u.Path,
		SourcePath: fmt.Sprintf("http://127.0.0.1:%d/json/version", port),
		SourceType: "json/version",
	}, nil
}

// FindChromeBinary locates a Chrome or Chromium executable on the local machine.
func FindChromeBinary() string {
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		pfx86 := os.Getenv("ProgramFiles(x86)")
		localAppData := os.Getenv("LOCALAPPDATA")
		candidates := []string{
			filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(pfx86, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(pf, "Chromium", "Application", "chrome.exe"),
		}
		for _, c := range candidates {
			if c != "" {
				if _, err := os.Stat(c); err == nil {
					return c
				}
			}
		}
		if p, err := exec.LookPath("chrome.exe"); err == nil {
			return p
		}
		return ""
	}

	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		candidates := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
		for _, c := range candidates {
			if c != "" {
				if _, err := os.Stat(c); err == nil {
					return c
				}
			}
		}
		return ""
	}

	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// LaunchIsolatedChromeDebug launches an isolated Chrome window with --remote-debugging-port enabled.
func LaunchIsolatedChromeDebug(port int) error {
	bin := FindChromeBinary()
	if bin == "" {
		return errors.New("no se encontró el ejecutable de Google Chrome en este equipo")
	}
	if port <= 0 || port > 65535 {
		port = 9222
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	profileDir := filepath.Join(cacheDir, "chrome-bridge-debug-profile")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	cmd := exec.Command(bin,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		fmt.Sprintf("--user-data-dir=%s", profileDir),
		"--no-first-run",
		"--no-default-browser-check",
	)
	return cmd.Start()
}
