// Package ipblock is a Traefik v3 middleware plugin (Yaegi-interpreted) that
// consults the ip-block.com decision API and blocks matching client IPs.
//
// Shared contract (all ip-block.com integrations):
//   - POST https://api.ip-block.com/v1/check
//   - JSON body: {api_key, site_id, ip, user_agent, referrer} (api_key in BODY)
//   - Content-Type: application/json
//   - 1s timeout
//   - block only when action == "block"
//   - fail open (allow) on any error/timeout/non-2xx/missing action (configurable)
//   - per-IP decision cache (default TTL 300s)
//   - whitelist of IPs never checked
//
// Yaegi note: this file sticks to the standard library only (net/http,
// encoding/json, time, sync, context, strings, net) so it is interpretable by
// Traefik's plugin engine. No third-party imports.
//
// Tested against Traefik v3.7.x.
package ipblock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config is the plugin configuration (populated from Traefik dynamic config).
type Config struct {
	Enabled          bool     `json:"enabled,omitempty"`
	SiteID           string   `json:"siteId,omitempty"`
	APIKey           string   `json:"apiKey,omitempty"`
	APIURL           string   `json:"apiUrl,omitempty"`
	FailOpen         bool     `json:"failOpen,omitempty"`
	CacheTTL         int      `json:"cacheTtl,omitempty"`   // seconds
	TimeoutMS        int      `json:"timeoutMs,omitempty"`  // milliseconds
	BehindProxy      bool     `json:"behindProxy,omitempty"`
	RealIPHeader     string   `json:"realIpHeader,omitempty"`
	BlockAction      string   `json:"blockAction,omitempty"` // "403" | "redirect"
	BlockRedirectURL string   `json:"blockRedirectUrl,omitempty"`
	BlockMessage     string   `json:"blockMessage,omitempty"`
	Whitelist        []string `json:"whitelist,omitempty"`
}

// CreateConfig returns the default configuration. Traefik calls this before
// unmarshalling the user's dynamic config on top.
func CreateConfig() *Config {
	return &Config{
		Enabled:          true,
		APIURL:           "https://api.ip-block.com/v1/check",
		FailOpen:         true,
		CacheTTL:         300,
		TimeoutMS:        1000,
		RealIPHeader:     "X-Forwarded-For",
		BlockAction:      "403",
		BlockRedirectURL: "https://www.ip-block.com/blocked.php",
		BlockMessage:     "Access denied.",
		Whitelist:        []string{},
	}
}

type cacheEntry struct {
	blocked bool
	expires time.Time
}

// IPBlock is the middleware handler.
type IPBlock struct {
	next         http.Handler
	name         string
	cfg          *Config
	whitelistSet map[string]struct{}
	client       *http.Client

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

// New creates a new IPBlock middleware. This is the Traefik plugin entrypoint.
func New(ctx context.Context, next http.Handler, config *Config, name string) (http.Handler, error) {
	if config == nil {
		config = CreateConfig()
	}
	if config.APIURL == "" {
		config.APIURL = "https://api.ip-block.com/v1/check"
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = 300
	}
	if config.TimeoutMS == 0 {
		config.TimeoutMS = 1000
	}
	if config.RealIPHeader == "" {
		config.RealIPHeader = "X-Forwarded-For"
	}
	if config.BlockAction == "" {
		config.BlockAction = "403"
	}
	if config.BlockRedirectURL == "" {
		config.BlockRedirectURL = "https://www.ip-block.com/blocked.php"
	}
	if config.BlockMessage == "" {
		config.BlockMessage = "Access denied."
	}
	if config.Enabled && config.SiteID == "" {
		return nil, fmt.Errorf("ip_block: siteId is required")
	}
	if config.Enabled && config.APIKey == "" {
		return nil, fmt.Errorf("ip_block: apiKey is required")
	}

	wl := make(map[string]struct{}, len(config.Whitelist))
	for _, ip := range config.Whitelist {
		wl[strings.TrimSpace(ip)] = struct{}{}
	}

	return &IPBlock{
		next:         next,
		name:         name,
		cfg:          config,
		whitelistSet: wl,
		client:       &http.Client{Timeout: time.Duration(config.TimeoutMS) * time.Millisecond},
		cache:        make(map[string]cacheEntry),
	}, nil
}

func (m *IPBlock) clientIP(r *http.Request) string {
	if m.cfg.BehindProxy {
		if h := r.Header.Get(m.cfg.RealIPHeader); h != "" {
			if i := strings.IndexByte(h, ','); i >= 0 {
				h = h[:i]
			}
			return strings.TrimSpace(h)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

type apiRequest struct {
	APIKey    string `json:"api_key"`
	SiteID    string `json:"site_id"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
	Referrer  string `json:"referrer"`
}

type apiResponse struct {
	Action string `json:"action"`
}

// checkAPI returns (blocked, ok). ok=false => infrastructure error (fail-open).
func (m *IPBlock) checkAPI(ctx context.Context, ip, ua, ref string) (bool, bool) {
	body, err := json.Marshal(apiRequest{
		APIKey:    m.cfg.APIKey,
		SiteID:    m.cfg.SiteID,
		IP:        ip,
		UserAgent: ua,
		Referrer:  ref,
	})
	if err != nil {
		return false, false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.APIURL, bytes.NewReader(body))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return false, false
	}
	var ar apiResponse
	if err := json.Unmarshal(data, &ar); err != nil || ar.Action == "" {
		return false, false
	}
	return ar.Action == "block", true
}

func (m *IPBlock) cacheGet(ip string) (bool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.cache[ip]
	if !ok || time.Now().After(e.expires) {
		return false, false
	}
	return e.blocked, true
}

func (m *IPBlock) cacheSet(ip string, blocked bool) {
	if m.cfg.CacheTTL <= 0 {
		return
	}
	m.mu.Lock()
	m.cache[ip] = cacheEntry{blocked: blocked, expires: time.Now().Add(time.Duration(m.cfg.CacheTTL) * time.Second)}
	m.mu.Unlock()
}

func (m *IPBlock) block(rw http.ResponseWriter, r *http.Request) {
	if m.cfg.BlockAction == "redirect" {
		http.Redirect(rw, r, m.cfg.BlockRedirectURL, http.StatusFound)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(rw, m.cfg.BlockMessage)
}

// ServeHTTP implements http.Handler.
func (m *IPBlock) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if !m.cfg.Enabled {
		m.next.ServeHTTP(rw, r)
		return
	}

	ip := m.clientIP(r)
	if ip == "" {
		if m.cfg.FailOpen {
			m.next.ServeHTTP(rw, r)
			return
		}
		m.block(rw, r)
		return
	}

	if _, ok := m.whitelistSet[ip]; ok {
		m.next.ServeHTTP(rw, r)
		return
	}

	if blocked, found := m.cacheGet(ip); found {
		if blocked {
			m.block(rw, r)
			return
		}
		m.next.ServeHTTP(rw, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(m.cfg.TimeoutMS)*time.Millisecond)
	defer cancel()

	blocked, ok := m.checkAPI(ctx, ip, r.UserAgent(), r.Referer())
	if !ok {
		// infrastructure error: fail-open policy, do not cache
		if m.cfg.FailOpen {
			m.next.ServeHTTP(rw, r)
			return
		}
		m.block(rw, r)
		return
	}

	m.cacheSet(ip, blocked)
	if blocked {
		m.block(rw, r)
		return
	}
	m.next.ServeHTTP(rw, r)
}
