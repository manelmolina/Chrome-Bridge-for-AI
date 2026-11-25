package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Profile stores non-sensitive connection preferences between launches.
type Profile struct {
	Host              string `json:"host"`
	Port              int    `json:"port"`
	Username          string `json:"username"`
	AuthType          string `json:"authType"`
	PrivateKeyPath    string `json:"privateKeyPath"`
	LocalForwardPorts string `json:"localForwardPorts"`
	StandardCDPPort   int    `json:"standardCdpPort"`
	FallbackDebugPort int    `json:"fallbackDebugPort"`
	CustomChromeDir   string `json:"customChromeDir"`
}

// Store persists user preferences and trusted SSH host keys in the OS config directory.
type Store struct {
	mu         sync.Mutex
	dir        string
	configPath string
	hostsPath  string
}

// NewStore creates a configuration store under the user's standard config directory.
func NewStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, err
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "chrome-bridge-for-ai")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{
		dir:        dir,
		configPath: filepath.Join(dir, "config.json"),
		hostsPath:  filepath.Join(dir, "known_hosts.json"),
	}, nil
}

// NewStoreInDir creates a configuration store in an explicit directory (useful for tests).
func NewStoreInDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{
		dir:        dir,
		configPath: filepath.Join(dir, "config.json"),
		hostsPath:  filepath.Join(dir, "known_hosts.json"),
	}, nil
}

// LoadProfile reads the saved profile or returns defaults if none exists yet.
func (s *Store) LoadProfile() Profile {
	s.mu.Lock()
	defer s.mu.Unlock()

	def := Profile{
		Port:              22,
		Username:          "root",
		AuthType:          "password",
		StandardCDPPort:   9222,
		FallbackDebugPort: 9222,
	}
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		return def
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return def
	}
	if p.AuthType != "password" && p.AuthType != "key" {
		p.AuthType = "password"
	}
	if p.Port <= 0 || p.Port > 65535 {
		p.Port = 22
	}
	if p.StandardCDPPort <= 0 || p.StandardCDPPort > 65535 {
		p.StandardCDPPort = 9222
	}
	if p.FallbackDebugPort <= 0 || p.FallbackDebugPort > 65535 {
		p.FallbackDebugPort = 9222
	}
	return p
}

// SaveProfile persists non-sensitive profile fields (passwords are never stored).
func (s *Store) SaveProfile(p Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.configPath, data, 0o600)
}

// LoadKnownHosts reads the map of "host:port" -> SHA256 fingerprint.
func (s *Store) LoadKnownHosts() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	hosts := make(map[string]string)
	data, err := os.ReadFile(s.hostsPath)
	if err != nil {
		return hosts
	}
	_ = json.Unmarshal(data, &hosts)
	if hosts == nil {
		return make(map[string]string)
	}
	return hosts
}

// SaveKnownHost records a trusted SSH host key fingerprint.
func (s *Store) SaveKnownHost(hostKeyID, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hosts := make(map[string]string)
	if data, err := os.ReadFile(s.hostsPath); err == nil {
		_ = json.Unmarshal(data, &hosts)
	}
	if hosts == nil {
		hosts = make(map[string]string)
	}
	hosts[hostKeyID] = fingerprint
	data, err := json.MarshalIndent(hosts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.hostsPath, data, 0o600)
}
