/*
Copyright 2026 The kcp Authors.

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
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCategorizeTLSError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "unknown authority error",
			err:      x509.UnknownAuthorityError{},
			expected: TLSErrorTypeUnknownCA,
		},
		{
			name:     "wrapped unknown authority error",
			err:      fmt.Errorf("connection failed: %w", x509.UnknownAuthorityError{}),
			expected: TLSErrorTypeUnknownCA,
		},
		{
			name:     "expired certificate error",
			err:      x509.CertificateInvalidError{Reason: x509.Expired},
			expected: TLSErrorTypeExpiredCert,
		},
		{
			name:     "wrapped expired certificate error",
			err:      fmt.Errorf("tls handshake: %w", x509.CertificateInvalidError{Reason: x509.Expired}),
			expected: TLSErrorTypeExpiredCert,
		},
		{
			name:     "other certificate invalid error",
			err:      x509.CertificateInvalidError{Reason: x509.NotAuthorizedToSign},
			expected: TLSErrorTypeOther,
		},
		{
			name:     "hostname error",
			err:      x509.HostnameError{},
			expected: TLSErrorTypeHostnameMismatch,
		},
		{
			name:     "tls record header error",
			err:      tls.RecordHeaderError{},
			expected: TLSErrorTypeCipherMismatch,
		},
		{
			name:     "generic error",
			err:      errors.New("some random error"),
			expected: "",
		},
		{
			name:     "connection refused",
			err:      errors.New("connection refused"),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := categorizeTLSError(tt.err)
			if got != tt.expected {
				t.Errorf("categorizeTLSError() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestIsTLSError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "unknown authority error",
			err:      x509.UnknownAuthorityError{},
			expected: true,
		},
		{
			name:     "certificate invalid error",
			err:      x509.CertificateInvalidError{},
			expected: true,
		},
		{
			name:     "hostname error",
			err:      x509.HostnameError{},
			expected: true,
		},
		{
			name:     "tls record header error",
			err:      tls.RecordHeaderError{},
			expected: true,
		},
		{
			name:     "wrapped tls error",
			err:      fmt.Errorf("connection failed: %w", x509.UnknownAuthorityError{}),
			expected: true,
		},
		{
			name:     "generic error",
			err:      errors.New("connection refused"),
			expected: false,
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isTLSError(tt.err)
			if got != tt.expected {
				t.Errorf("isTLSError() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestCountingReader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		readChunkSize int
		expectedBytes int64
	}{
		{
			name:          "empty body",
			input:         "",
			readChunkSize: 10,
			expectedBytes: 0,
		},
		{
			name:          "small body",
			input:         "hello",
			readChunkSize: 10,
			expectedBytes: 5,
		},
		{
			name:          "body read in chunks",
			input:         "hello world, this is a test",
			readChunkSize: 5,
			expectedBytes: 27,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reader := &countingReader{
				ReadCloser: io.NopCloser(strings.NewReader(tt.input)),
			}

			buf := make([]byte, tt.readChunkSize)
			for {
				_, err := reader.Read(buf)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if reader.bytesRead != tt.expectedBytes {
				t.Errorf("bytesRead = %d, want %d", reader.bytesRead, tt.expectedBytes)
			}
		})
	}
}

func TestResponseWriterDelegator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		writes        []string
		expectedBytes int64
	}{
		{
			name:          "no writes",
			writes:        nil,
			expectedBytes: 0,
		},
		{
			name:          "single write",
			writes:        []string{"hello"},
			expectedBytes: 5,
		},
		{
			name:          "multiple writes",
			writes:        []string{"hello", " ", "world"},
			expectedBytes: 11,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			delegator := &responseWriterDelegator{ResponseWriter: recorder}

			for _, s := range tt.writes {
				_, err := delegator.Write([]byte(s))
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			if delegator.bytesWritten != tt.expectedBytes {
				t.Errorf("bytesWritten = %d, want %d", delegator.bytesWritten, tt.expectedBytes)
			}

			// Verify the underlying writer received the data
			if recorder.Body.String() != strings.Join(tt.writes, "") {
				t.Errorf("body = %q, want %q", recorder.Body.String(), strings.Join(tt.writes, ""))
			}
		})
	}
}

func TestResponseWriterDelegator_Unwrap(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	delegator := &responseWriterDelegator{ResponseWriter: recorder}

	if delegator.Unwrap() != recorder {
		t.Error("Unwrap() should return the underlying ResponseWriter")
	}
}

func TestWithBodyTracking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		requestBody  string
		responseBody string
	}{
		{
			name:         "empty bodies",
			requestBody:  "",
			responseBody: "",
		},
		{
			name:         "request body only",
			requestBody:  "request data",
			responseBody: "",
		},
		{
			name:         "response body only",
			requestBody:  "",
			responseBody: "response data",
		},
		{
			name:         "both bodies",
			requestBody:  "request data",
			responseBody: "response data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a handler that reads the request body and writes a response
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Read the entire request body
				_, _ = io.ReadAll(r.Body)
				// Write the response
				if tt.responseBody != "" {
					_, _ = w.Write([]byte(tt.responseBody))
				}
			})

			// Wrap with body tracking
			wrapped := WithBodyTracking(handler)

			// Create request
			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader([]byte(tt.requestBody)))
			recorder := httptest.NewRecorder()

			// Execute
			wrapped.ServeHTTP(recorder, req)

			// Note: We can't directly verify the metrics were incremented without
			// exposing the counters or using the prometheus testutil package.
			// The structural tests above verify the counting logic works.
			// Here we just verify the handler executed correctly.
			if recorder.Body.String() != tt.responseBody {
				t.Errorf("response body = %q, want %q", recorder.Body.String(), tt.responseBody)
			}
		})
	}
}

func TestNewProxyErrorHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		expectedStatus int
	}{
		{
			name:           "tls error",
			err:            x509.UnknownAuthorityError{},
			expectedStatus: http.StatusBadGateway,
		},
		{
			name:           "non-tls error",
			err:            errors.New("connection refused"),
			expectedStatus: http.StatusBadGateway,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler := NewProxyErrorHandler()
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)

			handler(recorder, req, tt.err)

			if recorder.Code != tt.expectedStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tt.expectedStatus)
			}
		})
	}
}
