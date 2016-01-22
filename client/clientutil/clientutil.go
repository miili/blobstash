package clientutil

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// TODO(tsileo): reimport docstore-client in blobstash/client/docstore and split the other client in
// separate package using this package.

var transport http.RoundTripper = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	Dial: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).Dial,
	TLSHandshakeTimeout: 10 * time.Second,
}
var setupHTTP2Once sync.Once

func setupHTTP2() {
	if err := http2.ConfigureTransport(transport.(*http.Transport)); err != nil {
		// TODO(tsileo): add a enable HTTP 2 flag in opts?
		fmt.Printf("HTTP2 ERROR: %+v", err)
	}

}

type Opts struct {
	APIKey            string
	Headers           map[string]string
	Host              string
	UserAgent         string
	Namespace         string
	SnappyCompression bool
	EnableHTTP2       bool
}

func (opts *Opts) SetHost(host, apiKey string) *Opts {
	if host != "" {
		opts.Host = host
	}
	if apiKey != "" {
		opts.APIKey = apiKey
	}
	return opts
}

type Client struct {
	opts   *Opts
	client *http.Client
}

func New(opts *Opts) *Client {
	if opts == nil {
		panic("missing clientutil.Client opts")
	}
	if opts.EnableHTTP2 && strings.HasPrefix(opts.Host, "https") {
		setupHTTP2Once.Do(setupHTTP2)
	}
	client := &http.Client{
		Transport: transport,
	}
	return &Client{
		client: client,
		opts:   opts,
	}
}

func (client *Client) DoReq(method, path string, headers map[string]string, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequest(method, fmt.Sprintf("%s%s", client.opts.Host, path), body)
	if err != nil {
		return nil, err
	}

	if client.opts.APIKey != "" {
		request.SetBasicAuth("", client.opts.APIKey)
	}

	// Set our custom user agent
	if client.opts.UserAgent != "" {
		request.Header.Set("User-Agent", client.opts.UserAgent)
	}

	if client.opts.Namespace != "" {
		request.Header.Set("BlobStash-Namespace", client.opts.Namespace)
	}

	// Check if we should request compressed data
	if client.opts.SnappyCompression {
		request.Header.Set("Accept-Encoding", "snappy")
	}

	// Add custom headers
	for header, val := range client.opts.Headers {
		request.Header.Set(header, val)
	}
	for header, val := range headers {
		request.Header.Set(header, val)
	}
	return client.client.Do(request)
}
