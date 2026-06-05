/*
Copyright 2022 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"k8s.io/apiserver/pkg/endpoints/responsewriter"
	compbasemetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

// TLS error type constants for categorization.
const (
	TLSErrorTypeUnknownCA        = "unknown_ca"
	TLSErrorTypeExpiredCert      = "expired_cert"
	TLSErrorTypeCipherMismatch   = "cipher_mismatch"
	TLSErrorTypeHostnameMismatch = "hostname_mismatch"
	TLSErrorTypeOther            = "other"
)

// WithLatencyTracking tracks the number of seconds it took the wrapped handler
// to complete.
func WithLatencyTracking(delegate http.Handler) http.Handler {
	return promhttp.InstrumentHandlerDuration(requestLatencies.HistogramVec, delegate)
}

// TODO(csams): enhance metrics to include shard url.
var (
	requestLatencies = compbasemetrics.NewHistogramVec(
		&compbasemetrics.HistogramOpts{
			Name: "proxy_request_duration_seconds",
			Help: "Response latency distribution in seconds for each verb and HTTP response code.",
			Buckets: []float64{0.05, 0.1, 0.2, 0.4, 0.6, 0.8, 1.0, 1.25, 1.5, 2, 3,
				4, 5, 6, 8, 10, 15, 20, 30, 45, 60},
			StabilityLevel: compbasemetrics.ALPHA,
		},
		[]string{"method", "code"},
	)

	tlsConnectionErrors = compbasemetrics.NewCounterVec(
		&compbasemetrics.CounterOpts{
			Name:           "kcp_listener_tls_connection_error",
			Help:           "Total TLS connection failures to backend shards by error type.",
			StabilityLevel: compbasemetrics.ALPHA,
		},
		[]string{"shard", "error_type"},
	)

	backendRequestBodyBytes = compbasemetrics.NewCounterVec(
		&compbasemetrics.CounterOpts{
			Name:           "kcp_backend_rq_body_bytes",
			Help:           "Total bytes forwarded in request bodies from clients to backend.",
			StabilityLevel: compbasemetrics.ALPHA,
		},
		[]string{"shard"},
	)

	backendResponseBodyBytes = compbasemetrics.NewCounterVec(
		&compbasemetrics.CounterOpts{
			Name:           "kcp_backend_rs_body_bytes",
			Help:           "Total bytes returned in response bodies from backend to clients.",
			StabilityLevel: compbasemetrics.ALPHA,
		},
		[]string{"shard"},
	)
)

var registerMetrics sync.Once

// Register metrics.
func Register() {
	registerMetrics.Do(func() {
		legacyregistry.MustRegister(requestLatencies)
		legacyregistry.MustRegister(tlsConnectionErrors)
		legacyregistry.MustRegister(backendRequestBodyBytes)
		legacyregistry.MustRegister(backendResponseBodyBytes)
	})
}

func init() {
	Register()
}

// categorizeTLSError examines an error and returns the appropriate TLS error type label.
// Returns an empty string if the error is not TLS-related.
func categorizeTLSError(err error) string {
	// Check for unknown certificate authority
	var unknownAuthorityErr x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityErr) {
		return TLSErrorTypeUnknownCA
	}

	// Check for certificate validation errors (includes expired certs)
	var certInvalidErr x509.CertificateInvalidError
	if errors.As(err, &certInvalidErr) {
		if certInvalidErr.Reason == x509.Expired {
			return TLSErrorTypeExpiredCert
		}
		// Other certificate validation errors are still TLS errors
		return TLSErrorTypeOther
	}

	// Check for hostname/SAN mismatch errors
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return TLSErrorTypeHostnameMismatch
	}

	// Check for TLS record header errors (often indicates protocol/cipher issues)
	var recordHeaderErr tls.RecordHeaderError
	if errors.As(err, &recordHeaderErr) {
		return TLSErrorTypeCipherMismatch
	}

	// Not a recognized TLS error
	return ""
}

// isTLSError checks if the error is related to TLS.
func isTLSError(err error) bool {
	return categorizeTLSError(err) != ""
}

// NewProxyErrorHandler returns an error handler for httputil.ReverseProxy that
// tracks TLS connection errors and then delegates to the default behavior.
func NewProxyErrorHandler() func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		logger := klog.FromContext(r.Context())

		if isTLSError(err) {
			shardName := lookup.ShardNameFrom(r.Context())
			if shardName == "" {
				shardName = "unknown"
			}
			errorType := categorizeTLSError(err)
			tlsConnectionErrors.WithLabelValues(shardName, errorType).Inc()
			logger.V(4).Info("TLS connection error to backend", "shard", shardName, "type", errorType, "error", err)
		}

		// Default behavior: log the error and return 502 Bad Gateway
		logger.Error(err, "proxy error")
		w.WriteHeader(http.StatusBadGateway)
	}
}

// countingReader wraps an io.ReadCloser to count bytes read.
type countingReader struct {
	io.ReadCloser
	bytesRead int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

// responseWriterDelegator wraps an http.ResponseWriter to count bytes written.
type responseWriterDelegator struct {
	http.ResponseWriter
	bytesWritten int64
}

func (r *responseWriterDelegator) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += int64(n)
	return n, err
}

// Unwrap returns the underlying ResponseWriter, required for proper HTTP/2 support.
func (r *responseWriterDelegator) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// WithBodyTracking wraps the handler to track request and response body bytes.
// Only requests that are routed to a backend shard (i.e., have a shard name in context)
// are counted.
func WithBodyTracking(delegate http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Wrap request body to count incoming bytes
		reqCounter := &countingReader{ReadCloser: r.Body}
		r.Body = reqCounter

		// Wrap response writer to count outgoing bytes
		respDelegator := &responseWriterDelegator{ResponseWriter: w}
		wrappedWriter := responsewriter.WrapForHTTP1Or2(respDelegator)

		// Store a holder in context that inner handlers can update with the shard name.
		// This is necessary because inner handlers may create new request objects
		// with WithContext(), and we need to access the shard name after they return.
		ctx, holder := lookup.WithShardNameHolder(r.Context())
		r = r.WithContext(ctx)

		delegate.ServeHTTP(wrappedWriter, r)

		// Only record metrics for requests that were routed to a backend shard.
		// Requests handled locally by the front-proxy (e.g., /metrics, /readyz)
		// won't have a shard name set.
		shardName := holder.Name
		if shardName == "" {
			return
		}

		// Record metrics after request completes
		if reqCounter.bytesRead > 0 {
			backendRequestBodyBytes.WithLabelValues(shardName).Add(float64(reqCounter.bytesRead))
		}
		if respDelegator.bytesWritten > 0 {
			backendResponseBodyBytes.WithLabelValues(shardName).Add(float64(respDelegator.bytesWritten))
		}
	})
}
