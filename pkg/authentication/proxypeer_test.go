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

package authentication

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/authenticatorfactory"
	"k8s.io/apiserver/pkg/authentication/request/headerrequest"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
)

const testProxyName = "kcp-front-proxy"

func TestWithoutProxyPeerIdentity(t *testing.T) {
	t.Parallel()

	requestHeaderCA := newTestCA(t, "requestheader-ca")
	clientCA := newTestCA(t, "client-ca")

	testcases := map[string]struct {
		peer               *x509.Certificate
		usernameHeader     string
		expectPeerToRemain bool
	}{
		"proxy without username header": {
			peer: requestHeaderCA.issue(t, testProxyName),
		},
		"proxy with username header": {
			peer:               requestHeaderCA.issue(t, testProxyName),
			usernameHeader:     "user",
			expectPeerToRemain: true,
		},
		"requestheader CA cert with disallowed name": {
			peer:               requestHeaderCA.issue(t, "other"),
			expectPeerToRemain: true,
		},
		"client CA cert": {
			peer:               clientCA.issue(t, testProxyName),
			expectPeerToRemain: true,
		},
		"no peer certificate": {},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var delegatePeers []*x509.Certificate
			delegate := authenticator.RequestFunc(func(req *http.Request) (*authenticator.Response, bool, error) {
				if req.TLS != nil {
					delegatePeers = req.TLS.PeerCertificates
				}
				return nil, false, nil
			})

			authn := WithoutProxyPeerIdentity(delegate, &authenticatorfactory.RequestHeaderConfig{
				UsernameHeaders:    headerrequest.StaticStringSlice{"X-Remote-User"},
				CAContentProvider:  requestHeaderCA.contentProvider(t),
				AllowedClientNames: headerrequest.StaticStringSlice{testProxyName},
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
			if tc.peer != nil {
				req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tc.peer}}
			}
			if tc.usernameHeader != "" {
				req.Header.Set("X-Remote-User", tc.usernameHeader)
			}

			_, _, err := authn.AuthenticateRequest(req)
			require.NoError(t, err)

			if tc.expectPeerToRemain {
				require.Equal(t, []*x509.Certificate{tc.peer}, delegatePeers)
			} else {
				require.Empty(t, delegatePeers)
			}
			if tc.peer != nil {
				require.Equal(t, []*x509.Certificate{tc.peer}, req.TLS.PeerCertificates, "original request must not be modified")
			}
		})
	}
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, commonName string) testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return testCA{cert: cert, key: key}
}

// issue returns a client certificate for commonName signed by ca.
func (ca testCA) issue(t *testing.T, commonName string) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return cert
}

func (ca testCA) contentProvider(t *testing.T) dynamiccertificates.CAContentProvider {
	t.Helper()

	provider, err := dynamiccertificates.NewStaticCAContent(ca.cert.Subject.CommonName, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}))
	require.NoError(t, err)

	return provider
}
