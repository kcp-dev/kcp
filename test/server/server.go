/*
Copyright 2026 The KCP Authors.

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

package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/test/server/pki"
)

// TLSServer is a simple in-process HTTPS server for testing.
type TLSServer struct {
	server   *http.Server
	certFile string
	keyFile  string
	caFile   string
}

// NewTLSServer creates a new TLS server with auto-generated certificates.
// The certificates are saved in testdata/.{test-name}/.
func NewTLSServer(port int, hostnames []string, pkiDirectory string, handler http.Handler) (*TLSServer, error) {
	if len(hostnames) == 0 {
		return nil, fmt.Errorf("at least one hostname must be provided")
	}

	if err := os.MkdirAll(pkiDirectory, 0755); err != nil {
		return nil, fmt.Errorf("failed to create PKI directory: %w", err)
	}

	certFile := filepath.Join(pkiDirectory, "server.crt")
	keyFile := filepath.Join(pkiDirectory, "server.key")
	caFile := filepath.Join(pkiDirectory, "ca.crt")

	if err := pki.GenerateSelfSignedCert(certFile, keyFile, caFile, hostnames); err != nil {
		return nil, fmt.Errorf("failed to generate certificates: %w", err)
	}

	baseAddr := hostnames[0]

	return &TLSServer{
		server: &http.Server{
			Handler:           handler,
			Addr:              net.JoinHostPort(baseAddr, fmt.Sprintf("%d", port)),
			ReadTimeout:       5 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
		},
		certFile: certFile,
		keyFile:  keyFile,
		caFile:   caFile,
	}, nil
}

// Start starts the server on the given port and blocks until it's ready.
func (s *TLSServer) Start(ctx context.Context) error {
	go func() {
		if err := s.server.ListenAndServeTLS(s.certFile, s.keyFile); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// Wait for server to be ready
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, true, func(ctx context.Context) (bool, error) {
		conn, err := (&net.Dialer{Timeout: 1 * time.Second}).DialContext(ctx, "tcp", s.server.Addr)
		if err == nil {
			conn.Close()
			return true, nil
		}

		return false, nil
	})
}

// Stop stops the server.
func (s *TLSServer) Stop() error {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown server: %w", err)
		}
	}

	return nil
}

// CAFile returns the path to the CA certificate file.
func (s *TLSServer) CAFile() string {
	return s.caFile
}
