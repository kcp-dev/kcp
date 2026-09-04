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

package proxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/client-go/util/cert"

	serverproxy "github.com/kcp-dev/kcp/pkg/server/proxy"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

// writeMappingFile writes content to a mapping file in a temp dir and returns its path.
func writeMappingFile(t *testing.T, content string) string {
	t.Helper()

	file := filepath.Join(t.TempDir(), "mappings.yaml")
	require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
	return file
}

// writeSelfSignedCert writes a self-signed cert and key to a temp dir and returns their paths.
func writeSelfSignedCert(t *testing.T) (string, string) {
	t.Helper()

	certPEM, keyPEM, err := cert.GenerateSelfSignedCertKey("localhost", nil, nil)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile := filepath.Join(dir, "tls.crt")
	keyFile := filepath.Join(dir, "tls.key")
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))
	return certFile, keyFile
}

func TestLoadMappings(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		file    func(t *testing.T) string
		want    []types.PathMapping
		wantErr bool
	}{
		"empty filename yields nil mappings": {
			file: func(t *testing.T) string { return "" },
		},
		"missing file yields nil mappings": {
			file: func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing.yaml") },
		},
		"valid file yields mappings": {
			file: func(t *testing.T) string {
				return writeMappingFile(t, "- path: /services/\n  backend: https://localhost:7443\n")
			},
			want: []types.PathMapping{
				{
					Path:    "/services/",
					Backend: "https://localhost:7443",
				},
			},
		},
		"invalid YAML fails": {
			file:    func(t *testing.T) string { return writeMappingFile(t, "{invalid") },
			wantErr: true,
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			mappings, err := loadMappings(cas.file(t))
			if cas.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, cas.want, mappings)
		})
	}
}

func TestNewHandler_noMappings(t *testing.T) {
	t.Parallel()

	handler, err := NewHandler(t.Context(), nil)
	require.NoError(t, err)

	httpHandler, ok := handler.(*serverproxy.HttpHandler)
	require.Truef(t, ok, "expected *proxy.HttpHandler, got %T", handler)

	assert.Nil(t, httpHandler.DefaultHandler, "no mappings must not install a default handler")
	assert.Empty(t, httpHandler.Mappings, "no mappings must not install routes")
}

func TestNewHandler_buildsConfiguredMappings(t *testing.T) {
	t.Parallel()

	certFile, keyFile := writeSelfSignedCert(t)
	mappings := []types.PathMapping{
		{
			Path:            "/services/",
			Backend:         "https://localhost:7443",
			BackendServerCA: certFile,
			ProxyClientCert: certFile,
			ProxyClientKey:  keyFile,
		},
		{
			Path:            "/",
			Backend:         "https://localhost:8443",
			BackendServerCA: certFile,
			ProxyClientCert: certFile,
			ProxyClientKey:  keyFile,
		},
	}

	handler, err := NewHandler(t.Context(), mappings)
	require.NoError(t, err)

	httpHandler, ok := handler.(*serverproxy.HttpHandler)
	require.Truef(t, ok, "expected *proxy.HttpHandler, got %T", handler)

	require.Len(t, httpHandler.Mappings, 1, "the / mapping must become the default handler, not a route")
	assert.Equal(t, "/services/", httpHandler.Mappings[0].Path)
	assert.NotNil(t, httpHandler.DefaultHandler, "the / mapping must become the default handler")
}
