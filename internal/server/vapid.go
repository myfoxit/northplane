package server

import (
	"context"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/notify"
)

func modelNewSecret(n int) string { return model.NewSecret(n) }

// initVAPID loads or generates the Web Push keypair (ADR-12) and installs
// it into the notifier; the public key is exposed to the SPA for
// PushManager.subscribe.
func (s *Server) initVAPID(ctx context.Context) {
	var stored map[string]string
	if err := s.Store.KVGet(ctx, "vapid", &stored); err == nil && stored["d"] != "" {
		if v, err := notify.UnmarshalVAPID(stored); err == nil {
			notify.SetVAPID(v)
			return
		}
	}
	subject := "mailto:ops@northplane.local"
	if s.Cfg.BaseURL != "" {
		subject = s.Cfg.BaseURL
	}
	v, err := notify.GenerateVAPID(subject)
	if err != nil {
		s.Log.Warn("server: VAPID generation failed, web push disabled", "err", err)
		return
	}
	notify.SetVAPID(v)
	_ = s.Store.KVPut(ctx, "vapid", notify.MarshalVAPID(v))
}

// VAPIDPublicKey exposes the key for the UI bootstrap.
func (s *Server) VAPIDPublicKey() string {
	var stored map[string]string
	if err := s.Store.KVGet(context.Background(), "vapid", &stored); err == nil {
		if v, err := notify.UnmarshalVAPID(stored); err == nil {
			return v.PublicKeyB64()
		}
	}
	return ""
}
