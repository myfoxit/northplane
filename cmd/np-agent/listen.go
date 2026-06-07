// Active listener mode (NCPA equivalent, SPEC §8.4): a small HTTPS API
// the Northplane server queries via the builtin `agent` check. Always
// TLS (self-signed on the fly when no cert is configured — pair with
// the check's --insecure flag or pin the cert), bearer-token auth,
// read-only metrics plus execution of the checks ALREADY configured in
// agent.yaml (allowlist — no arbitrary remote commands).
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// metricsPayload is the /v1/metrics shape consumed by checks/agent.go —
// keep both sides in sync.
type metricsPayload struct {
	Agent     string        `json:"agent"`
	Version   string        `json:"version"`
	Hostname  string        `json:"hostname"`
	UptimeSec int64         `json:"uptimeSeconds"`
	CPUs      int           `json:"cpus"`
	Load1     *float64      `json:"load1,omitempty"`
	Memory    *memMetrics   `json:"memory,omitempty"`
	Disks     []diskMetrics `json:"disks,omitempty"`
}

type memMetrics struct {
	UsedPct        float64 `json:"usedPct"`
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
}

type diskMetrics struct {
	Mount     string  `json:"mount"`
	UsedPct   float64 `json:"usedPct"`
	FreeBytes uint64  `json:"freeBytes"`
}

type runPayload struct {
	Service string `json:"service"`
	State   int    `json:"state"`
	Output  string `json:"output"`
}

// listenerHandler builds the active-listener API (separated from the
// TLS/server plumbing so tests can drive it directly).
func listenerHandler(cfg agentConfig) http.Handler {
	mux := http.NewServeMux()

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(tok), []byte(cfg.ListenToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("GET /v1/health", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"agent": "np-agent", "version": version,
			"hostname": cfg.Hostname, "uptimeSeconds": int64(time.Since(startTime).Seconds())})
	}))

	mux.HandleFunc("GET /v1/metrics", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, collectMetrics(cfg))
	}))

	// Run a check from agent.yaml by service name (allowlist semantics).
	mux.HandleFunc("GET /v1/run/{service}", auth(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("service")
		for _, chk := range cfg.Checks {
			if chk.Service != name {
				continue
			}
			state, output := runPluginCheck(r.Context(), chk)
			writeJSON(w, runPayload{Service: name, State: state, Output: output})
			return
		}
		http.Error(w, "unknown check (not in agent.yaml)", http.StatusNotFound)
	}))
	return mux
}

func serveListener(ctx context.Context, cfg agentConfig, log *slog.Logger) error {
	mux := listenerHandler(cfg)
	tlsCfg, err := listenerTLS(cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: mux, TLSConfig: tlsCfg,
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	log.Info("np-agent: active listener", "addr", cfg.Listen,
		"tls", map[bool]string{true: "configured", false: "self-signed"}[cfg.TLSCert != ""])
	if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func collectMetrics(cfg agentConfig) metricsPayload {
	m := metricsPayload{Agent: "np-agent", Version: version, Hostname: cfg.Hostname,
		UptimeSec: int64(time.Since(startTime).Seconds()), CPUs: runtime.NumCPU()}
	if load, ok := loadAvg(); ok {
		m.Load1 = &load
	}
	if usedPct, total, avail, ok := memUsage(); ok {
		m.Memory = &memMetrics{UsedPct: usedPct, TotalBytes: total, AvailableBytes: avail}
	}
	for _, mount := range cfg.Disk {
		if usedPct, freeBytes, ok := diskUsage(mount); ok {
			m.Disks = append(m.Disks, diskMetrics{Mount: mount, UsedPct: usedPct, FreeBytes: freeBytes})
		}
	}
	return m
}

// listenerTLS loads the configured cert or mints an in-memory
// self-signed one (valid 10 years, CN = hostname).
func listenerTLS(cfg agentConfig) (*tls.Config, error) {
	if cfg.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cfg.Hostname, Organization: []string{"np-agent"}},
		DNSNames:     []string{cfg.Hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}, nil
}
