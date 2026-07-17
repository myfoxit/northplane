package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/auth"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/selector"
	"github.com/northplane/northplane/internal/storage"
)

// Discovery scans (SPEC §11.3 /discovery/scans): TCP-probe sweeps over
// a CIDR producing host suggestions for the wizard/NL-config flows.
// ICMP sweeps need privileges; the TCP fallback covers the common case.

type discoveryScan struct {
	ID        string         `json:"id"`
	TenantID  string         `json:"tenantId"`
	CIDR      string         `json:"cidr"`
	Ports     []int          `json:"ports"`
	Status    string         `json:"status"` // running|done|failed
	StartedAt time.Time      `json:"startedAt"`
	DoneAt    *time.Time     `json:"doneAt,omitempty"`
	Found     []discoveryHit `json:"found,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type discoveryHit struct {
	Address   string   `json:"address"`
	Hostname  string   `json:"hostname,omitempty"`
	OpenPorts []int    `json:"openPorts"`
	Suggest   []string `json:"suggest"` // suggested service checks
}

var (
	scansMu sync.RWMutex
	scans   = map[string]*discoveryScan{}
)

func (a *API) registerDiscovery() {
	type scanRequest struct {
		CIDR  string `json:"cidr"`
		Ports []int  `json:"ports,omitempty"`
	}
	a.handle("POST /api/v1/discovery/scans", "Start a network discovery scan",
		"config:write", scanRequest{}, discoveryScan{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			var req scanRequest
			if !a.decode(w, r, &req) {
				return
			}
			_, ipnet, err := net.ParseCIDR(req.CIDR)
			if err != nil {
				a.validationError(w, r, "cidr", err.Error())
				return
			}
			ones, bits := ipnet.Mask.Size()
			if bits-ones > 12 {
				a.validationError(w, r, "cidr", "scan limited to /20 or smaller")
				return
			}
			// SSRF guard: refuse loopback / link-local (incl. the cloud
			// metadata endpoint 169.254.169.254) / multicast / unspecified
			// targets so the scanner can't be turned inward. Private RFC1918
			// ranges remain allowed — they are the normal monitoring case.
			if ip := ipnet.IP; ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
				ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
				a.validationError(w, r, "cidr", "refusing to scan loopback/link-local/multicast range")
				return
			}
			if len(req.Ports) == 0 {
				req.Ports = []int{22, 80, 443, 3389, 5432, 3306, 8080}
			}
			scan := &discoveryScan{ID: model.NewID(), TenantID: a.tenantOf(r, p),
				CIDR: req.CIDR, Ports: req.Ports, Status: "running",
				StartedAt: time.Now().UTC()}
			scansMu.Lock()
			scans[scan.ID] = scan
			scansMu.Unlock()
			a.audit(r, p, "discovery.scan", scan.ID, nil, req)
			go a.runScan(scan, ipnet)
			a.writeJSON(w, http.StatusAccepted, scan)
		}).Status(http.StatusAccepted)

	a.handle("GET /api/v1/discovery/scans", "List scans", "objects:read", nil, listResponse{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			tenant := a.tenantOf(r, p)
			scansMu.RLock()
			var out []*discoveryScan
			for _, s := range scans {
				if s.TenantID == tenant {
					out = append(out, s)
				}
			}
			scansMu.RUnlock()
			sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
			a.writeList(w, out, "")
		})

	a.handle("GET /api/v1/discovery/scans/{id}", "Scan status + suggestions", "objects:read", nil, discoveryScan{},
		func(w http.ResponseWriter, r *http.Request, p *auth.Principal) {
			scansMu.RLock()
			scan := scans[param(r, "id")]
			scansMu.RUnlock()
			if scan == nil || scan.TenantID != a.tenantOf(r, p) {
				a.problem(w, r, http.StatusNotFound, "np:not-found", "scan not found", "")
				return
			}
			a.writeJSON(w, http.StatusOK, scan)
		})
}

func (a *API) runScan(scan *discoveryScan, ipnet *net.IPNet) {
	// This runs in a detached goroutine (not under HTTP middleware recovery),
	// so a panic here would crash the whole process. Recover, mark the scan
	// failed, and let the server keep running.
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("discovery: scan panic recovered",
				"scan", scan.ID, "panic", r, "stack", string(debug.Stack()))
			scansMu.Lock()
			now := time.Now().UTC()
			scan.Status, scan.DoneAt = "failed", &now
			scansMu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for ip := firstIP(ipnet); ipnet.Contains(ip); ip = nextIP(ip) {
		addr := ip.String()
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer func() { <-sem; wg.Done() }()
			var open []int
			for _, port := range scan.Ports {
				d := net.Dialer{Timeout: 800 * time.Millisecond}
				conn, err := d.DialContext(ctx, "tcp",
					net.JoinHostPort(addr, fmt.Sprint(port)))
				if err == nil {
					_ = conn.Close()
					open = append(open, port)
				}
			}
			if len(open) == 0 {
				return
			}
			hit := discoveryHit{Address: addr, OpenPorts: open}
			if names, err := net.LookupAddr(addr); err == nil && len(names) > 0 {
				hit.Hostname = names[0]
			}
			for _, port := range open {
				switch port {
				case 22:
					hit.Suggest = append(hit.Suggest, "builtin:ssh-banner")
				case 80:
					hit.Suggest = append(hit.Suggest, "builtin:http -p 80")
				case 443:
					hit.Suggest = append(hit.Suggest, "builtin:http -S", "builtin:tls-cert")
				case 5432:
					hit.Suggest = append(hit.Suggest, "builtin:tcp -p 5432 (postgres)")
				case 3306:
					hit.Suggest = append(hit.Suggest, "builtin:tcp -p 3306 (mysql)")
				default:
					hit.Suggest = append(hit.Suggest, fmt.Sprintf("builtin:tcp -p %d", port))
				}
			}
			mu.Lock()
			scan.Found = append(scan.Found, hit)
			mu.Unlock()
		}(addr)
	}
	wg.Wait()
	scansMu.Lock()
	now := time.Now().UTC()
	scan.Status, scan.DoneAt = "done", &now
	sort.Slice(scan.Found, func(i, j int) bool { return scan.Found[i].Address < scan.Found[j].Address })
	scansMu.Unlock()
	a.Log.Info("discovery: scan done", "cidr", scan.CIDR, "hits", len(scan.Found))
}

func firstIP(ipnet *net.IPNet) net.IP {
	ip := ipnet.IP.Mask(ipnet.Mask)
	return nextIP(ip) // skip network address
}

func nextIP(ip net.IP) net.IP {
	out := make(net.IP, len(ip))
	copy(out, ip)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			break
		}
	}
	return out
}

// webhook subscriptions (SPEC §11.5): stored as resources; the
// dispatcher worker forwards matching events through the outbox.
func (a *API) registerWebhookSubs() {
	a.resourceCRUD("webhooks", storage.KindWebhookSub, "config", WebhookSubscription{})
}

// WebhookSubscription configures an outgoing event webhook.
type WebhookSubscription struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Types    []string `json:"types,omitempty"`    // event types, empty = all
	Selector string   `json:"selector,omitempty"` // label filter
	Secret   string   `json:"secret,omitempty"`   // $SECRET:name$ ref or literal
	Disabled bool     `json:"disabled,omitempty"`
}

// WebhookDispatcher subscribes to the bus and enqueues deliveries.
func (a *API) WebhookDispatcher(ctx context.Context) {
	sub := a.Bus.Subscribe(1024)
	defer sub.Close()
	type cachedSubs struct {
		subs    []*WebhookSubscription
		fetched time.Time
	}
	cache := map[string]*cachedSubs{}
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-sub.C:
			c := cache[e.TenantID]
			if c == nil || time.Since(c.fetched) > 30*time.Second {
				subs, err := storage.LoadAll[WebhookSubscription](ctx, a.Store, e.TenantID, storage.KindWebhookSub)
				if err != nil {
					continue
				}
				c = &cachedSubs{subs: subs, fetched: time.Now()}
				cache[e.TenantID] = c
			}
			for _, ws := range c.subs {
				if ws.Disabled {
					continue
				}
				if len(ws.Types) > 0 && !containsStr(ws.Types, string(e.Type)) {
					continue
				}
				// Selector filters on the event's payload labels (ingress
				// events, alert_opened, …). A non-empty selector that fails
				// to parse matches nothing rather than everything.
				if ws.Selector != "" {
					sel, err := selector.Parse(ws.Selector)
					if err != nil || !sel.Matches(eventLabels(e)) {
						continue
					}
				}
				body, _ := json.Marshal(e)
				payload, _ := json.Marshal(map[string]any{
					"url": ws.URL, "secret": ws.Secret, "body": json.RawMessage(body)})
				_ = a.Store.EnqueueOutbox(ctx, &storage.OutboxItem{
					TenantID: e.TenantID, Kind: "webhook-sub", Payload: payload})
			}
		}
	}
}

// eventLabels extracts the labels map from an event payload (NormEvent
// ingress events and alert lifecycle events both carry "labels").
func eventLabels(e *model.Event) map[string]string {
	var p struct {
		Labels map[string]string `json:"labels"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	if p.Labels == nil {
		return map[string]string{}
	}
	return p.Labels
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
