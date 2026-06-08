package notify

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"

	"github.com/northplane/northplane/internal/model"
)

// Web Push (ADR-12): dependency-less mobile alerting to the PWA —
// RFC 8291 aes128gcm payload encryption + RFC 8292 VAPID auth, built on
// stdlib crypto + x/crypto/hkdf.

// VAPID carries the server identification keypair.
type VAPID struct {
	Private *ecdsa.PrivateKey
	Subject string // mailto: or https: contact
}

// VAPIDKeys is injected into the Manager by the server.
var vapid *VAPID

// SetVAPID installs the server keypair.
func SetVAPID(v *VAPID) { vapid = v }

// PublicKeyB64 renders the uncompressed public key for the browser's
// PushManager.subscribe(applicationServerKey).
func (v *VAPID) PublicKeyB64() string {
	return base64.RawURLEncoding.EncodeToString(vapidPublicBytes(v))
}

// vapidPublicBytes returns the uncompressed (0x04 || X || Y) P-256 public
// key encoding required by RFC 8292, via crypto/ecdh (PublicKey.Bytes is
// the non-deprecated equivalent of elliptic.Marshal for P-256). The key is
// always a freshly generated P-256 key, so the conversion cannot fail in
// practice; if it ever did, an empty slice is returned rather than a panic.
func vapidPublicBytes(v *VAPID) []byte {
	ecdhPub, err := v.Private.PublicKey.ECDH()
	if err != nil {
		return nil
	}
	return ecdhPub.Bytes()
}

// GenerateVAPID creates a fresh keypair.
func GenerateVAPID(subject string) (*VAPID, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &VAPID{Private: key, Subject: subject}, nil
}

// MarshalVAPID/UnmarshalVAPID persist the keypair in the kv store.
func MarshalVAPID(v *VAPID) map[string]string {
	return map[string]string{
		"d":       base64.RawURLEncoding.EncodeToString(v.Private.D.Bytes()),
		"x":       base64.RawURLEncoding.EncodeToString(v.Private.X.Bytes()),
		"y":       base64.RawURLEncoding.EncodeToString(v.Private.Y.Bytes()),
		"subject": v.Subject,
	}
}

func UnmarshalVAPID(m map[string]string) (*VAPID, error) {
	d, err1 := base64.RawURLEncoding.DecodeString(m["d"])
	x, err2 := base64.RawURLEncoding.DecodeString(m["x"])
	y, err3 := base64.RawURLEncoding.DecodeString(m["y"])
	if err1 != nil || err2 != nil || err3 != nil {
		return nil, fmt.Errorf("bad vapid key encoding")
	}
	key := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(),
			X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)},
		D: new(big.Int).SetBytes(d),
	}
	return &VAPID{Private: key, Subject: m["subject"]}, nil
}

// PushSubscription mirrors the browser's PushSubscription.toJSON().
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// sendPush delivers to every subscription of the contact's user.
func (m *Manager) sendPush(ctx context.Context, ch *model.NotificationChannel,
	userID, body string, rc *RenderContext) (string, error) {
	if vapid == nil {
		return "", fmt.Errorf("web push: VAPID keys not initialised")
	}
	if userID == "" {
		return "", fmt.Errorf("web push: contact is not linked to a user")
	}
	rows, err := m.store.DB().QueryContext(ctx, m.store.Q(
		`SELECT id, endpoint, keys FROM push_subscriptions WHERE user_id = ?`), userID)
	if err != nil {
		return "", err
	}
	type subRow struct {
		id  string
		sub PushSubscription
	}
	var subs []subRow
	for rows.Next() {
		var id, endpoint, keys string
		if err := rows.Scan(&id, &endpoint, &keys); err != nil {
			rows.Close()
			return "", err
		}
		var s PushSubscription
		s.Endpoint = endpoint
		_ = json.Unmarshal([]byte(keys), &s.Keys)
		subs = append(subs, subRow{id, s})
	}
	rows.Close()
	if len(subs) == 0 {
		return "", fmt.Errorf("web push: no subscriptions for user")
	}

	payload, _ := json.Marshal(map[string]string{
		"title": "[" + rc.Severity + "] Northplane",
		"body":  body, "url": rc.AlertURL, "ackUrl": rc.AckURL,
		"severity": strings.ToLower(rc.Severity),
	})
	var lastErr error
	delivered := 0
	for _, s := range subs {
		if err := webPushSend(ctx, vapid, &s.sub, payload); err != nil {
			lastErr = err
			// 404/410 = subscription gone: clean up
			if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), "HTTP 410") {
				_, _ = m.store.DB().ExecContext(ctx, m.store.Q(
					`DELETE FROM push_subscriptions WHERE id = ?`), s.id)
			}
			continue
		}
		delivered++
	}
	if delivered == 0 && lastErr != nil {
		return "", lastErr
	}
	return fmt.Sprintf("delivered=%d", delivered), nil
}

// webPushSend encrypts per RFC 8291 and posts with VAPID auth.
func webPushSend(ctx context.Context, v *VAPID, sub *PushSubscription, plaintext []byte) error {
	clientPubRaw, err := base64.RawURLEncoding.DecodeString(padless(sub.Keys.P256dh))
	if err != nil {
		return fmt.Errorf("bad p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(padless(sub.Keys.Auth))
	if err != nil {
		return fmt.Errorf("bad auth: %w", err)
	}
	curve := ecdh.P256()
	clientPub, err := curve.NewPublicKey(clientPubRaw)
	if err != nil {
		return fmt.Errorf("bad client key: %w", err)
	}
	asKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	shared, err := asKey.ECDH(clientPub)
	if err != nil {
		return err
	}
	asPub := asKey.PublicKey().Bytes()

	// RFC 8291 key derivation
	prkKey := hkdf.Extract(sha256.New, shared, authSecret)
	keyInfo := append([]byte("WebPush: info\x00"), append(clientPubRaw, asPub...)...)
	ikm := hkdfExpand(prkKey, keyInfo, 32)

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	prk := hkdf.Extract(sha256.New, ikm, salt)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	// last-record padding delimiter 0x02
	ciphertext := gcm.Seal(nil, nonce, append(plaintext, 0x02), nil)

	// aes128gcm body: salt | rs | idlen | keyid | ciphertext
	body := &bytes.Buffer{}
	body.Write(salt)
	// Record size into the in-memory buffer; a fixed-size uint32 into a
	// bytes.Buffer cannot fail.
	_ = binary.Write(body, binary.BigEndian, uint32(4096))
	body.WriteByte(byte(len(asPub)))
	body.Write(asPub)
	body.Write(ciphertext)

	jwt, err := vapidJWT(v, sub.Endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, body)
	if err != nil {
		return err
	}
	pub := vapidPublicBytes(v)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "high")
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+base64.RawURLEncoding.EncodeToString(pub))
	resp, err := hookClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024)) // drain for keep-alive
	if resp.StatusCode >= 300 {
		return fmt.Errorf("push endpoint: HTTP %d", resp.StatusCode)
	}
	return nil
}

func hkdfExpand(prk, info []byte, length int) []byte {
	out := make([]byte, length)
	r := hkdf.Expand(sha256.New, prk, info)
	if _, err := io.ReadFull(r, out); err != nil {
		// Only happens if length exceeds 255*HashLen; all call sites request
		// 12/16/32 bytes, so a failure here would be a programming error that
		// must not silently yield a short (weakened) key.
		panic(fmt.Sprintf("webpush: hkdf expand %d bytes: %v", length, err))
	}
	return out
}

// vapidJWT signs the ES256 token for the endpoint origin (RFC 8292).
func vapidJWT(v *VAPID, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	sub := v.Subject
	if sub == "" {
		sub = "mailto:ops@northplane.local"
	}
	claims, _ := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": time.Now().Add(12 * time.Hour).Unix(),
		"sub": sub,
	})
	payload := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(payload))
	r, s, err := ecdsa.Sign(rand.Reader, v.Private, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return payload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func padless(s string) string { return strings.TrimRight(s, "=") }

func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
