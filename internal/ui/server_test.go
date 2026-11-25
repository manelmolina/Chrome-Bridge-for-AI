package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"antigravity-chrome-tunnel/internal/config"
	"antigravity-chrome-tunnel/internal/tunnel"
)

func TestServerAuthAndEndpoints(t *testing.T) {
	store, err := config.NewStoreInDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := tunnel.NewManager(store)
	defer mgr.Stop()

	srv, err := NewServer(mgr, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	base := "http://" + srv.listener.Addr().String()

	// Unauthorized without token.
	resp, err := http.Get(base + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}

	// Authorized with token.
	req, _ := http.NewRequest(http.MethodGet, base+"/api/status", nil)
	req.Header.Set("X-App-Token", srv.token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d", resp.StatusCode)
	}
	var snap tunnel.StatusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.State != tunnel.StateDisconnected {
		t.Fatalf("expected disconnected state, got %s", snap.State)
	}

	// Root HTML requires token query param.
	respRoot, err := http.Get(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	_ = respRoot.Body.Close()
	if respRoot.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on authenticated root URL, got %d", respRoot.StatusCode)
	}

	// Connect validation error returns 400.
	badReq, _ := http.NewRequest(http.MethodPost, base+"/api/connect?token="+srv.token, strings.NewReader(`{"host":""}`))
	respBad, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty host, got %d", respBad.StatusCode)
	}

	// SSH keys endpoint returns 200 JSON array.
	keysReq, _ := http.NewRequest(http.MethodGet, base+"/api/ssh-keys", nil)
	keysReq.Header.Set("X-App-Token", srv.token)
	keysResp, err := http.DefaultClient.Do(keysReq)
	if err != nil {
		t.Fatal(err)
	}
	defer keysResp.Body.Close()
	if keysResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /api/ssh-keys, got %d", keysResp.StatusCode)
	}
	var keysPayload struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(keysResp.Body).Decode(&keysPayload); err != nil {
		t.Fatal(err)
	}
}
