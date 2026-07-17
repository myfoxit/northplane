package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/northplane/northplane/internal/model"
)

// MQTT output channel: publishes the rendered notification body (default
// template: alert JSON) to a broker topic — building automation, DECT
// gateways, sirens and other MQTT consumers subscribe to it.
//
// Config keys:
//
//	url        broker (tcp://host:1883, ssl://host:8883, ws://, wss://) — required
//	topic      publish topic; {severity} and {alertId} expand — required
//	username   optional
//	password   optional ($SECRET ref supported)
//	qos        0|1|2 (default 1)
//	retain     "true" retains the message
//	clientId   default "northplane-notify"
//	tlsInsecure "true" skips certificate verification
func (m *Manager) sendMQTT(ctx context.Context, ch *model.NotificationChannel,
	body string, rc *RenderContext) (string, error) {
	broker := ch.Config["url"]
	topic := ch.Config["topic"]
	if broker == "" || topic == "" {
		return "", fmt.Errorf("mqtt: config url and topic required")
	}
	if rc != nil && rc.Alert != nil {
		topic = strings.NewReplacer(
			"{severity}", strings.ToLower(rc.Severity),
			"{alertId}", rc.Alert.ID,
		).Replace(topic)
	}
	qos := byte(1)
	if v, err := strconv.Atoi(ch.Config["qos"]); err == nil && v >= 0 && v <= 2 {
		qos = byte(v)
	}
	clientID := ch.Config["clientId"]
	if clientID == "" {
		clientID = "northplane-notify"
	}

	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(clientID)
	opts.SetConnectTimeout(10 * time.Second)
	opts.SetWriteTimeout(10 * time.Second)
	opts.SetAutoReconnect(false) // one-shot publish; the outbox retries
	if u := ch.Config["username"]; u != "" {
		opts.SetUsername(u)
		opts.SetPassword(m.resolveSecret(ch.TenantID, ch.Config["password"]))
	}
	if ch.Config["tlsInsecure"] == "true" {
		opts.SetTLSConfig(&tls.Config{InsecureSkipVerify: true})
	}

	client := mqtt.NewClient(opts)
	defer client.Disconnect(250)
	if tok := client.Connect(); !tok.WaitTimeout(15*time.Second) || tok.Error() != nil {
		return "", fmt.Errorf("mqtt connect: %w", tokenErr(tok))
	}
	pub := client.Publish(topic, qos, ch.Config["retain"] == "true", []byte(body))
	if !pub.WaitTimeout(15*time.Second) || pub.Error() != nil {
		return "", fmt.Errorf("mqtt publish: %w", tokenErr(pub))
	}
	_ = ctx
	return topic, nil
}

func tokenErr(t mqtt.Token) error {
	if err := t.Error(); err != nil {
		return err
	}
	return fmt.Errorf("timeout")
}
