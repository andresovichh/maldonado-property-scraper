package discovery

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Response is a fetched page, already read into memory and capped.
type Response struct {
	URL     string
	Status  int
	Body    string
	Headers string
}

// Fetcher is a polite HTTP client: one request at a time per host, a delay between
// them, a hard cap on body size, and no retries beyond a single one on transport
// errors. We are scanning ~150 small business websites — being slow is free, being
// rude is not.
type Fetcher struct {
	client   *http.Client
	delay    time.Duration
	maxBytes int64

	mu    sync.Mutex
	hosts map[string]*hostGate
}

type hostGate struct {
	mu   sync.Mutex
	last time.Time
}

// NewFetcher builds a Fetcher. delay is the minimum gap between two requests to the
// same host.
func NewFetcher(timeout, delay time.Duration) *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
				// Many of these sites are small shops with expired or mismatched
				// certificates. We are reading public marketing pages, so a broken
				// chain is a reason to note it, not to skip the agency entirely.
				TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // public pages only
				MaxIdleConnsPerHost: 2,
				DisableKeepAlives:   false,
			},
		},
		delay:    delay,
		maxBytes: 3 << 20, // 3 MiB is plenty for a listing page
		hosts:    map[string]*hostGate{},
	}
}

func (f *Fetcher) gate(host string) *hostGate {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.hosts[host]
	if !ok {
		g = &hostGate{}
		f.hosts[host] = g
	}
	return g
}

// Get fetches a URL, serialising per host and honouring the configured delay.
func (f *Fetcher) Get(ctx context.Context, raw string) (*Response, error) {
	host := HostOf(raw)
	if host == "" {
		return nil, fmt.Errorf("bad url %q", raw)
	}

	g := f.gate(host)
	g.mu.Lock()
	if wait := f.delay - time.Since(g.last); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			g.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	defer func() {
		g.last = time.Now()
		g.mu.Unlock()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-UY,es;q=0.9,en;q=0.8")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes))
	if err != nil && len(b) == 0 {
		return nil, err
	}

	var hdr strings.Builder
	for _, k := range []string{"Server", "X-Powered-By", "X-Generator", "Content-Type"} {
		if v := resp.Header.Get(k); v != "" {
			fmt.Fprintf(&hdr, "%s: %s\n", k, v)
		}
	}

	return &Response{
		URL:     resp.Request.URL.String(),
		Status:  resp.StatusCode,
		Body:    string(b),
		Headers: hdr.String(),
	}, nil
}
