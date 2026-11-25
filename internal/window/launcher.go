package window

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Launch opens the desktop UI window pointing to url and returns a wait channel if a dedicated window process is spawned.
func Launch(url string) (<-chan error, error) {
	profileDir := uiProfileDir()
	_ = os.MkdirAll(profileDir, 0o700)

	if bin := findAppBrowser(); bin != "" {
		args := []string{
			"--app=" + url,
			"--user-data-dir=" + profileDir,
			"--window-size=1120,720",
			"--start-maximized",
			"--no-first-run",
			"--no-default-browser-check",
		}
		if runtime.GOOS == "linux" {
			args = append(args, "--class=ChromeBridgeForAI")
		}
		cmd := exec.Command(bin, args...)
		if err := cmd.Start(); err == nil {
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			return done, nil
		}
	}

	if runtime.GOOS == "linux" {
		if done, ok := launchLinuxWebKit(url); ok {
			return done, nil
		}
		cmd := exec.Command("xdg-open", url)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("no se pudo abrir la ventana de la aplicación: %w", err)
		}
		return nil, nil
	}

	if runtime.GOOS == "darwin" {
		cmd := exec.Command("open", url)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("no se pudo abrir la ventana de la aplicación: %w", err)
		}
		return nil, nil
	}

	if runtime.GOOS == "windows" {
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("no se pudo abrir la ventana de la aplicación: %w", err)
		}
		return nil, nil
	}

	return nil, fmt.Errorf("sistema operativo no soportado para abrir ventana: %s", runtime.GOOS)
}

func uiProfileDir() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "chrome-bridge-for-ai-ui")
}

func findAppBrowser() string {
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		pfx86 := os.Getenv("ProgramFiles(x86)")
		localAppData := os.Getenv("LOCALAPPDATA")
		candidates := []string{
			filepath.Join(pfx86, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(pf, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(pf, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(pfx86, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(pf, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		}
		for _, c := range candidates {
			if c != "" {
				if _, err := os.Stat(c); err == nil {
					return c
				}
			}
		}
		for _, name := range []string{"msedge.exe", "chrome.exe", "brave.exe"} {
			if p, err := exec.LookPath(name); err == nil {
				return p
			}
		}
		return ""
	}

	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		candidates := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"),
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
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

	for _, name := range []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
		"brave-browser",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func launchLinuxWebKit(url string) (<-chan error, bool) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil, false
	}
	script := `
import sys, gi
gi.require_version("Gtk", "3.0")
gi.require_version("WebKit2", "4.1")
from gi.repository import Gtk, WebKit2
win = Gtk.Window(title="Chrome Bridge for AI")
win.set_default_size(1120, 720)
win.maximize()
win.connect("destroy", Gtk.main_quit)
wv = WebKit2.WebView()
wv.load_uri(sys.argv[1])
win.add(wv)
win.show_all()
Gtk.main()
`
	cmd := exec.Command(py, "-c", script, url)
	if err := cmd.Start(); err != nil {
		return nil, false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, true
}
