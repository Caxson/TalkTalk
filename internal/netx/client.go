// Package netx builds network clients with an explicit proxy policy.
//
// All of TalkTalk's default providers are domestic endpoints that must be
// reached directly, while the developer's shell often exports HTTP_PROXY for
// overseas traffic. Inheriting that proxy silently would route domestic calls
// abroad, so the policy is always an explicit choice.
package netx

import (
	"net"
	"net/http"
	"time"
)

// NewHTTPClient returns a client suited to long-lived streaming responses:
// no overall timeout (callers bound requests with a context), but bounded
// dial and header phases. useEnvProxy honours HTTP(S)_PROXY / NO_PROXY.
func NewHTTPClient(useEnvProxy bool) *http.Client {
	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if useEnvProxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr}
}

// NewWebSocketHTTPClient returns a client for WebSocket handshakes. The
// upgrade must run over HTTP/1.1, so HTTP/2 negotiation is disabled.
func NewWebSocketHTTPClient(useEnvProxy bool) *http.Client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
	}
	if useEnvProxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr}
}
