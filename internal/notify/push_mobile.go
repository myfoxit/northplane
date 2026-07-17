package notify

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// Mobile push for the northplane-alarm app (server contract in the app
// README): subscriptions registered as fcm://<device-token> or
// apns://<device-token> ride the same push_subscriptions table as Web
// Push; the push channel's config carries the provider credentials.
//
// push channel config keys:
//   fcmServiceAccount   Firebase service-account JSON ($SECRET ref
//                       recommended); project_id, client_email,
//                       private_key, token_uri are used
//   fcmEndpoint         override for tests
//   apnsKey             APNs auth key, .p8 PEM ($SECRET ref recommended)
//   apnsKeyId           key id (10 chars)
//   apnsTeamId          Apple team id
//   apnsTopic           bundle id (e.g. com.northplane.alarm)
//   apnsSandbox         "true" → api.sandbox.push.apple.com
//
// Alarm-app sound steering (labels on the alert): np.sound
// (np_klaxon|np_sirene|np_puls), np.volume (0.0–1.0), np.overrideSilent
// ("true" → APNs critical alert / FCM high-priority with the same keys
// in data).

// errSubGone marks device tokens the provider reports as unregistered —
// the caller deletes the subscription row.
var errSubGone = fmt.Errorf("subscription gone")

// mobileTokens caches short-lived provider bearer tokens per channel.
var mobileTokens sync.Map // cache key → *cachedToken

type cachedToken struct {
	token string
	exp   time.Time
}

// sendMobilePush routes one subscription endpoint to FCM or APNs.
func (m *Manager) sendMobilePush(ctx context.Context, ch *model.NotificationChannel,
	endpoint, body string, rc *RenderContext) error {
	switch {
	case strings.HasPrefix(endpoint, "fcm://"):
		return m.sendFCM(ctx, ch, strings.TrimPrefix(endpoint, "fcm://"), body, rc)
	case strings.HasPrefix(endpoint, "apns://"):
		return m.sendAPNs(ctx, ch, strings.TrimPrefix(endpoint, "apns://"), body, rc)
	default:
		return fmt.Errorf("unknown mobile push scheme")
	}
}

// pushData is the common data payload the alarm app consumes.
func pushData(rc *RenderContext, body string) map[string]string {
	d := map[string]string{
		"type":     "alert_opened",
		"title":    "[" + rc.Severity + "] Northplane",
		"body":     body,
		"severity": strings.ToLower(rc.Severity),
		"url":      rc.AlertURL,
		"ackUrl":   rc.AckURL,
	}
	if rc.Alert != nil {
		d["alertId"] = rc.Alert.ID
		if len(rc.Alert.Labels) > 0 {
			if raw, err := json.Marshal(rc.Alert.Labels); err == nil {
				d["labels"] = string(raw)
			}
			for _, k := range []string{"np.sound", "np.volume", "np.overrideSilent"} {
				if v := rc.Alert.Labels[k]; v != "" {
					d[k] = v
				}
			}
		}
	}
	return d
}

// --- FCM HTTP v1 -------------------------------------------------------

func (m *Manager) sendFCM(ctx context.Context, ch *model.NotificationChannel,
	deviceToken, body string, rc *RenderContext) error {
	saJSON := m.resolveSecret(ch.TenantID, ch.Config["fcmServiceAccount"])
	if saJSON == "" {
		return fmt.Errorf("fcm: channel config fcmServiceAccount missing")
	}
	var sa struct {
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
		return fmt.Errorf("fcm: service account JSON: %w", err)
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	bearer, err := m.fcmBearer(ctx, ch.ID, &sa)
	if err != nil {
		return err
	}

	data := pushData(rc, body)
	msg := map[string]any{
		"message": map[string]any{
			"token": deviceToken,
			"notification": map[string]string{
				"title": data["title"], "body": body,
			},
			"android": map[string]any{
				"priority": "HIGH",
			},
			"data": data,
		},
	}
	raw, _ := json.Marshal(msg)
	endpoint := ch.Config["fcmEndpoint"]
	if endpoint == "" {
		endpoint = "https://fcm.googleapis.com/v1/projects/" + url.PathEscape(sa.ProjectID) + "/messages:send"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound || strings.Contains(string(rbody), "UNREGISTERED") {
		return errSubGone
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("fcm: HTTP %d: %s", resp.StatusCode, firstLine(string(rbody)))
	}
	return nil
}

// fcmBearer exchanges a service-account JWT for an OAuth2 access token
// (plain two-legged flow — no SDK dependency) and caches it.
func (m *Manager) fcmBearer(ctx context.Context, channelID string, sa *struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}) (string, error) {
	cacheKey := "fcm/" + channelID
	if v, ok := mobileTokens.Load(cacheKey); ok {
		if t := v.(*cachedToken); time.Now().Before(t.exp) {
			return t.token, nil
		}
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("fcm: service account private_key is not PEM")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// some service accounts ship PKCS#1
		if k1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
			key = k1
		} else {
			return "", fmt.Errorf("fcm: parse private key: %w", err)
		}
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("fcm: private key is not RSA")
	}

	now := time.Now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss":   sa.ClientEmail,
		"scope": "https://www.googleapis.com/auth/firebase.messaging",
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	assertion := signing + "." + base64.RawURLEncoding.EncodeToString(sig)

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := hookClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("fcm token: HTTP %d: %s", resp.StatusCode, firstLine(string(rbody)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(rbody, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("fcm token: bad response")
	}
	exp := time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 5*time.Minute)
	mobileTokens.Store(cacheKey, &cachedToken{token: tok.AccessToken, exp: exp})
	return tok.AccessToken, nil
}

// --- APNs (token-based auth, HTTP/2) ------------------------------------

func (m *Manager) sendAPNs(ctx context.Context, ch *model.NotificationChannel,
	deviceToken, body string, rc *RenderContext) error {
	keyPEM := m.resolveSecret(ch.TenantID, ch.Config["apnsKey"])
	keyID := ch.Config["apnsKeyId"]
	teamID := ch.Config["apnsTeamId"]
	topic := ch.Config["apnsTopic"]
	if keyPEM == "" || keyID == "" || teamID == "" || topic == "" {
		return fmt.Errorf("apns: channel config apnsKey, apnsKeyId, apnsTeamId, apnsTopic required")
	}
	bearer, err := apnsBearer(ch.ID, keyPEM, keyID, teamID)
	if err != nil {
		return err
	}

	data := pushData(rc, body)
	aps := map[string]any{
		"alert":              map[string]string{"title": data["title"], "body": body},
		"interruption-level": "time-sensitive",
	}
	// Sound steering per app contract: np.sound picks the bundled tone,
	// np.overrideSilent=true asks for a critical alert (needs Apple's
	// critical-alerts entitlement; falls back gracefully in the app),
	// np.volume sets the critical-alert volume.
	sound := data["np.sound"]
	if sound != "" {
		sound += ".caf"
	} else {
		sound = "default"
	}
	if data["np.overrideSilent"] == "true" {
		vol := 1.0
		if v, err := strconv.ParseFloat(data["np.volume"], 64); err == nil && v >= 0 && v <= 1 {
			vol = v
		}
		aps["sound"] = map[string]any{"critical": 1, "name": sound, "volume": vol}
		aps["interruption-level"] = "critical"
	} else {
		aps["sound"] = sound
	}
	payload := map[string]any{"aps": aps}
	for k, v := range data {
		if k != "title" && k != "body" {
			payload[k] = v
		}
	}
	raw, _ := json.Marshal(payload)

	host := "https://api.push.apple.com"
	if ch.Config["apnsSandbox"] == "true" {
		host = "https://api.sandbox.push.apple.com"
	}
	if override := ch.Config["apnsEndpoint"]; override != "" {
		host = override // tests
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		host+"/3/device/"+url.PathEscape(deviceToken), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", "0")
	resp, err := hookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode == http.StatusGone || strings.Contains(string(rbody), "BadDeviceToken") ||
		strings.Contains(string(rbody), "Unregistered") {
		return errSubGone
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("apns: HTTP %d: %s", resp.StatusCode, firstLine(string(rbody)))
	}
	return nil
}

// apnsBearer signs (and caches) the ES256 provider token.
func apnsBearer(channelID, keyPEM, keyID, teamID string) (string, error) {
	cacheKey := "apns/" + channelID
	if v, ok := mobileTokens.Load(cacheKey); ok {
		if t := v.(*cachedToken); time.Now().Before(t.exp) {
			return t.token, nil
		}
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return "", fmt.Errorf("apns: key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("apns: parse key: %w", err)
	}
	ecKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return "", fmt.Errorf("apns: key is not EC (p8 expected)")
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": keyID})
	claims, _ := json.Marshal(map[string]any{"iss": teamID, "iat": now.Unix()})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, ecKey, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	token := signing + "." + base64.RawURLEncoding.EncodeToString(sig)
	// Apple accepts tokens up to 60 min; refresh after 50.
	mobileTokens.Store(cacheKey, &cachedToken{token: token, exp: now.Add(50 * time.Minute)})
	return token, nil
}
