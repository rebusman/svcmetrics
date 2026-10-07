package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// HTTPObserver sends every event to a remote server as a JSON POST request.
type HTTPObserver struct {
	url    string
	client *http.Client
	// safeURL is url without userinfo, path and query, which may carry
	// credentials or tokens; it is the only form of the URL put in errors.
	safeURL string
}

var _ Observer = (*HTTPObserver)(nil)

// ErrInvalidURL reports an audit URL that is not an absolute http or https URL.
var ErrInvalidURL = errors.New("audit URL must be an absolute http or https URL")

// NewHTTPObserver checks that rawURL is an absolute http or https URL, so that
// a typo fails at startup rather than with the first request. A nil client
// means a client of the observer's own, built by [newHTTPClient]; the delivery
// is bounded by the context the [Publisher] passes in any case.
//
// A rejected URL is reported as [ErrInvalidURL] alone. Neither the URL nor the
// parser's explanation goes into the error: the URL may carry credentials or
// tokens, and the explanation quotes pieces of it — a password mistaken for a
// port, say, when the @ is missing.
func NewHTTPObserver(rawURL string, client *http.Client) (*HTTPObserver, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrInvalidURL
	}
	if client == nil {
		client = newHTTPClient()
	}
	safeURL := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	return &HTTPObserver{url: rawURL, client: client, safeURL: safeURL}, nil
}

// newHTTPClient builds the client an observer uses when given none. It has a
// transport of its own rather than [http.DefaultTransport], so the delivery is
// not affected by whatever else in the process changes or exhausts the shared
// one, and a timeout of [DefaultDeliveryTimeout] on top of the context.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: DefaultDeliveryTimeout,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// Name returns the scheme and host events are sent to. The rest of the URL is
// left out, since it may hold credentials or tokens and Name ends up in logs.
func (o *HTTPObserver) Name() string { return "url " + o.safeURL }

// Update posts the event and treats any status other than 2xx as a failure.
func (o *HTTPObserver) Update(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build audit request: %w", o.redact(err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("send audit event: %w", o.redact(err))
	}
	defer resp.Body.Close()
	// Draining the body lets the client reuse the connection.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("send audit event: unexpected status %s", resp.Status)
	}
	return nil
}

// redact replaces the URL that the http package puts in its errors with the
// safe form, keeping the rest of the error intact.
func (o *HTTPObserver) redact(err error) error {
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		return err
	}
	return &url.Error{Op: uerr.Op, URL: o.safeURL, Err: uerr.Err}
}
