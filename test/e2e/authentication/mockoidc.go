/*
Copyright 2025 The KCP Authors.

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
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"github.com/xrstf/mockoidc"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	"github.com/kcp-dev/kcp/sdk/testing/third_party/library-go/crypto"
)

func startMockOIDC(t *testing.T, server kcptestingserver.RunningServer) (*mockoidc.MockOIDC, *crypto.CA) {
	// start a mock OIDC server that will listen on a random port
	// (only for discovery and keyset handling, no actual login workflows)
	caDir := server.CADirectory()
	caCertFile := filepath.Join(caDir, "mockoidc-ca.crt")
	caKeyFile := filepath.Join(caDir, "mockoidc-ca.key")

	ca, _, err := crypto.EnsureCA(caCertFile, caKeyFile, "", "mockoidc-ca", 1)
	require.NoError(t, err)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca.Config.Certs[0])

	cert, err := ca.MakeServerCert(sets.New("localhost"), 30, func(c *x509.Certificate) error {
		c.IPAddresses = []net.IP{
			net.ParseIP("127.0.0.1"),
		}
		return nil
	})
	require.NoError(t, err)

	tlsCert := tls.Certificate{
		Certificate: [][]byte{},
		PrivateKey:  cert.Key,
	}

	for _, c := range cert.Certs {
		tlsCert.Certificate = append(tlsCert.Certificate, c.Raw)
	}

	tlsConfig := &tls.Config{
		RootCAs:      caPool,
		ServerName:   "localhost",
		Certificates: []tls.Certificate{tlsCert},
	}

	m, err := mockoidc.RunTLS(tlsConfig)
	require.NoError(t, err)

	return m, ca
}

func mockJWTAuthenticator(t *testing.T, m *mockoidc.MockOIDC, ca *crypto.CA) tenancyv1alpha1.JWTAuthenticator {
	cfg := m.Config()

	caCert, _, err := ca.Config.GetPEMBytes()
	require.NoError(t, err)

	return tenancyv1alpha1.JWTAuthenticator{
		Issuer: tenancyv1alpha1.Issuer{
			URL:                  cfg.Issuer,
			Audiences:            []string{cfg.ClientID},
			CertificateAuthority: string(caCert),
		},
		ClaimMappings: tenancyv1alpha1.ClaimMappings{
			Username: tenancyv1alpha1.PrefixedClaimOrExpression{
				Claim:  "email",
				Prefix: ptr.To("oidc:"),
			},
			Groups: tenancyv1alpha1.PrefixedClaimOrExpression{
				Claim:  "groups",
				Prefix: ptr.To("oidc:"),
			},
		},
	}
}

func createOIDCToken(t *testing.T, mock *mockoidc.MockOIDC, subject, email string, groups []string) string {
	var (
		cfg                 = mock.Config()
		now                 = mockoidc.NowFunc()
		scope               = "openid groups email"
		nonce               = "noften"
		codeChallenge       = "nothing-to-see-here"
		codeChallengeMethod = cfg.CodeChallengeMethodsSupported[0]
	)

	session, err := mock.SessionStore.NewSession(scope, nonce, &mockoidc.MockUser{
		Subject: subject,
		Email:   email,
		Groups:  groups,
	}, codeChallenge, codeChallengeMethod)
	require.NoError(t, err)

	// session.IDToken does not allow to customize the audience claim, so we have to build it by hand.
	// To access kcp, a token needs to have both the kcp audience and match the audience configured
	// in the WorkspaceAuthConfig object.
	claims, err := session.User.Claims(session.Scopes, &mockoidc.IDTokenClaims{
		RegisteredClaims: &jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{
				cfg.ClientID,
				"https://kcp.default.svc",
			},
			ExpiresAt: jwt.NewNumericDate(now.Add(cfg.AccessTTL)),
			ID:        session.SessionID,
			IssuedAt:  jwt.NewNumericDate(now),
			Issuer:    cfg.Issuer,
			NotBefore: jwt.NewNumericDate(now),
			Subject:   session.User.ID(),
		},
		Nonce: nonce,
	})
	require.NoError(t, err)

	token, err := mock.Keypair.SignJWT(claims)
	require.NoError(t, err)

	return token
}
