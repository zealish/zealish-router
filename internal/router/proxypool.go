package router

import (
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/zealish/zealish-router/internal/storage"
)

// ProxyPool rotates outbound requests across the enabled proxies of the pool.
// A nil or empty pool means direct connections.
type ProxyPool struct {
	urls []*url.URL
	next atomic.Uint64
}

// NewProxyPool builds a pool from stored proxy records, skipping disabled
// entries and URLs that do not parse. The order is the stored order.
func NewProxyPool(records []storage.Proxy) *ProxyPool {
	pool := &ProxyPool{}
	for _, rec := range records {
		if !rec.Enabled {
			continue
		}
		u, err := url.Parse(rec.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		pool.urls = append(pool.urls, u)
	}
	return pool
}

// Size reports how many usable proxies the pool holds.
func (p *ProxyPool) Size() int {
	if p == nil {
		return 0
	}
	return len(p.urls)
}

// pick returns the next proxy in round-robin order, or nil for an empty pool.
func (p *ProxyPool) pick() *url.URL {
	if p.Size() == 0 {
		return nil
	}
	n := p.next.Add(1) - 1
	return p.urls[n%uint64(len(p.urls))]
}

// transport builds an HTTP transport that routes each request through the
// next proxy in the pool. An empty pool yields nil, meaning the default
// direct transport.
func (p *ProxyPool) transport() http.RoundTripper {
	if p.Size() == 0 {
		return nil
	}
	return &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) { return p.pick(), nil },
	}
}
