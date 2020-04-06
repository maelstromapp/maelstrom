package common

import (
	"golang.org/x/net/http2"
	"net"
	"net/http"
	"time"
)

type HTTPClientSettings struct {
	ConnectTimeout        time.Duration
	ConnKeepAlive         time.Duration
	ExpectContinue        time.Duration
	IdleConnTimeout       time.Duration
	MaxAllIdleConns       int
	MaxHostIdleConns      int
	ResponseHeaderTimeout time.Duration
	TLSHandshakeTimeout   time.Duration
}

func NewHTTPClientWithSettings(httpSettings HTTPClientSettings) (*http.Client, error) {
	tr := &http.Transport{
		ResponseHeaderTimeout: httpSettings.ResponseHeaderTimeout,
		Proxy:                 http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			KeepAlive: httpSettings.ConnKeepAlive,
			DualStack: true,
			Timeout:   httpSettings.ConnectTimeout,
		}).DialContext,
		MaxIdleConns:          httpSettings.MaxAllIdleConns,
		IdleConnTimeout:       httpSettings.IdleConnTimeout,
		TLSHandshakeTimeout:   httpSettings.TLSHandshakeTimeout,
		MaxIdleConnsPerHost:   httpSettings.MaxHostIdleConns,
		ExpectContinueTimeout: httpSettings.ExpectContinue,
	}

	// So client makes HTTP/2 requests
	err := http2.ConfigureTransport(tr)
	if err != nil {
		return nil, err
	}

	return &http.Client{
		Transport: tr,
	}, nil
}
