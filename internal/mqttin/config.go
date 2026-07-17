package mqttin

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/northplane/northplane/internal/model"
)

// defaultQoS is the subscription QoS when Config["qos"] is unset.
const defaultQoS byte = 1

// mqttConfig is the parsed, validated transport config for one source.
type mqttConfig struct {
	url         string
	topics      []string
	qos         byte
	clientID    string
	username    string
	passwordRef string // secret reference (preferred)
	passwordLit string // inline fallback Config["password"]
	tlsInsecure bool
	severity    model.Severity
}

// parseConfig reads the source Config map (SPEC §7.5 mqtt fields):
// url (tcp://host:1883, ssl://host:8883, ws://, wss://), topics
// (comma-separated filters), qos (0/1/2, default 1), clientId, username,
// password / passwordSecretRef, tlsInsecure, severity.
func parseConfig(src *model.EventSource) (mqttConfig, error) {
	cfg := src.Config
	c := mqttConfig{qos: defaultQoS, severity: defaultSeverity(src)}
	c.url = strings.TrimSpace(cfg["url"])
	if c.url == "" {
		return c, fmt.Errorf("url required")
	}
	u, err := url.Parse(c.url)
	if err != nil {
		return c, fmt.Errorf("invalid url %q: %w", c.url, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "tcp", "mqtt", "ssl", "tls", "mqtts", "ws", "wss":
	default:
		return c, fmt.Errorf("unsupported url scheme %q (use tcp:// ssl:// ws:// wss://)", u.Scheme)
	}
	if u.Host == "" {
		return c, fmt.Errorf("url %q has no host", c.url)
	}
	for _, t := range strings.Split(cfg["topics"], ",") {
		if t = strings.TrimSpace(t); t != "" {
			c.topics = append(c.topics, t)
		}
	}
	if len(c.topics) == 0 {
		return c, fmt.Errorf("topics required (comma-separated topic filters)")
	}
	if q := strings.TrimSpace(cfg["qos"]); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 || n > 2 {
			return c, fmt.Errorf("invalid qos %q (0, 1 or 2)", q)
		}
		c.qos = byte(n)
	}
	c.clientID = strings.TrimSpace(cfg["clientId"])
	if c.clientID == "" {
		c.clientID = defaultClientID(src.ID)
	}
	c.username = cfg["username"]
	c.passwordRef = strings.TrimSpace(cfg["passwordSecretRef"])
	c.passwordLit = cfg["password"]
	c.tlsInsecure = strings.EqualFold(strings.TrimSpace(cfg["tlsInsecure"]), "true")
	return c, nil
}

// defaultClientID derives a stable broker client ID from the source ID.
func defaultClientID(id string) string {
	if len(id) > 8 {
		id = id[:8]
	}
	return "northplane-" + id
}

// secure reports whether the broker URL uses a TLS transport (where
// tlsInsecure applies).
func (c mqttConfig) secure() bool {
	switch {
	case strings.HasPrefix(c.url, "ssl://"),
		strings.HasPrefix(c.url, "tls://"),
		strings.HasPrefix(c.url, "mqtts://"),
		strings.HasPrefix(c.url, "wss://"):
		return true
	}
	return false
}

// fingerprint identifies a config so the Manager can detect changes that
// warrant a connection restart. The secret value is not read here; we
// include its reference and the inline literal so credential changes
// restart too. Mapping, labels and rate limit are event-shaping and the
// runner captures them at start, so they are part of the fingerprint.
func (c mqttConfig) fingerprint(src *model.EventSource) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d|%s|%s|%s|%s|%t|%s|%s|%s|%g|%d\n",
		c.url, strings.Join(c.topics, ","), c.qos, c.clientID,
		c.username, c.passwordRef, c.passwordLit, c.tlsInsecure, c.severity,
		src.Labels.String(), mappingString(src.Mapping), src.RateLimit, src.Burst)
	return hex.EncodeToString(h.Sum(nil))
}

// mappingString renders the CEL mapping as "k=v,k2=v2" sorted by key
// (stable for hashing, mirrors model.Labels.String).
func mappingString(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
	}
	return b.String()
}

// clientOptions assembles the pure transport options for a parsed config
// (no credentials, no handlers, no I/O — unit-testable).
func clientOptions(c mqttConfig) *mqtt.ClientOptions {
	opts := mqtt.NewClientOptions().
		AddBroker(c.url).
		SetClientID(c.clientID).
		SetCleanSession(true).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(connectRetryInterval).
		SetKeepAlive(keepAlive)
	if c.tlsInsecure && c.secure() {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true}) // #nosec G402 -- explicit operator opt-in
	}
	return opts
}
