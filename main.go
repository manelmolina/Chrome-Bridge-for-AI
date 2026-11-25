package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"antigravity-chrome-tunnel/internal/config"
	"antigravity-chrome-tunnel/internal/tunnel"
	"antigravity-chrome-tunnel/internal/ui"
	"antigravity-chrome-tunnel/internal/window"
)

func main() {
	headless := flag.Bool("headless", false, "Ejecutar sin ventana gráfica (modo terminal)")
	host := flag.String("host", "", "IP o dominio del servidor")
	port := flag.Int("port", 22, "Puerto SSH")
	user := flag.String("user", "root", "Usuario SSH")
	password := flag.String("password", "", "Contraseña SSH")
	keyPath := flag.String("key", "", "Ruta a la clave privada SSH (alternativa a --password)")
	keyPass := flag.String("key-passphrase", "", "Passphrase de la clave SSH (opcional)")
	localForwards := flag.String("forward", "", "Puertos locales opcionales servidor->PC (ej. 3000,5173)")
	cdpPort := flag.Int("cdp-port", 9222, "Puerto CDP estándar en el servidor (para Codex, Claude Code, Playwright)")
	flag.Parse()

	store, err := config.NewStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Advertencia: no se pudo abrir almacenamiento de configuración: %v\n", err)
	}

	mgr := tunnel.NewManager(store)

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	if *headless {
		if *host == "" || (*password == "" && *keyPath == "") {
			fmt.Fprintln(os.Stderr, "En modo --headless debes indicar --host, --user y --password o --key")
			os.Exit(1)
		}
		authType := "password"
		if *keyPath != "" {
			authType = "key"
		}
		ch, unsub := mgr.Subscribe()
		defer unsub()

		if err := mgr.Start(tunnel.ConnectRequest{
			Host:              *host,
			Port:              *port,
			Username:          *user,
			AuthType:          authType,
			Password:          *password,
			PrivateKeyPath:    *keyPath,
			KeyPassphrase:     *keyPass,
			LocalForwardPorts: *localForwards,
			StandardCDPPort:   *cdpPort,
			FallbackDebugPort: 9222,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		printed := 0
		for {
			select {
			case <-sigCh:
				mgr.Stop()
				return
			case snap := <-ch:
				for i := printed; i < len(snap.Logs); i++ {
					fmt.Printf("[%s] [%s] %s\n", snap.Logs[i].Time, snap.Logs[i].Level, snap.Logs[i].Message)
				}
				printed = len(snap.Logs)
				if snap.State == tunnel.StateError {
					mgr.Stop()
					os.Exit(1)
				}
			}
		}
	}

	closeCh := make(chan struct{}, 1)
	triggerClose := func() {
		select {
		case closeCh <- struct{}{}:
		default:
		}
	}

	srv, err := ui.NewServer(mgr, store, triggerClose)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error iniciando interfaz local: %v\n", err)
		os.Exit(1)
	}
	srv.Start()

	winDone, err := window.Launch(srv.URL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Abre manualmente en tu navegador: %s\n", srv.URL())
	}

	if winDone != nil {
		go func() {
			<-winDone
			// Wait slightly to avoid exiting early if browser handed off to an existing process.
			time.Sleep(1500 * time.Millisecond)
			snap := mgr.Snapshot()
			if snap.State == tunnel.StateDisconnected || snap.State == tunnel.StateError {
				triggerClose()
			}
		}()
	}

	select {
	case <-sigCh:
	case <-closeCh:
	}

	mgr.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
