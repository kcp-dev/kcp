/*
Copyright 2022 The KCP Authors.

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

package embeddedetcd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/wal"
	"go.uber.org/zap"

	"github.com/kcp-dev/kcp/pkg/embeddedetcd/options"
)

type Config struct {
	*embed.Config
}

func NewConfig(o options.CompletedOptions, enableWatchCache bool) (*Config, error) {
	if o.WalSizeBytes != 0 {
		wal.SegmentSizeBytes = o.WalSizeBytes
	}

	cfg := embed.NewConfig()

	cfg.Logger = "zap"
	cfg.LogLevel = "warn"

	cfg.Dir = o.Directory
	cfg.AuthToken = ""

	cfg.ListenPeerUrls = []url.URL{{Scheme: "https", Host: "localhost:" + o.PeerPort}}
	cfg.AdvertisePeerUrls = []url.URL{{Scheme: "https", Host: "localhost:" + o.PeerPort}}
	cfg.ListenClientUrls = []url.URL{{Scheme: "https", Host: "localhost:" + o.ClientPort}}
	cfg.AdvertiseClientUrls = []url.URL{{Scheme: "https", Host: "localhost:" + o.ClientPort}}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)

	if err := fileutil.TouchDirAll(zap.NewNop(), cfg.Dir); err != nil {
		return nil, err
	}

	if err := generateClientAndServerCerts([]string{"localhost"}, filepath.Join(cfg.Dir, "secrets")); err != nil {
		return nil, err
	}
	cfg.PeerTLSInfo.ServerName = "localhost"
	cfg.PeerTLSInfo.CertFile = filepath.Join(cfg.Dir, "secrets", "peer", "cert.pem")
	cfg.PeerTLSInfo.KeyFile = filepath.Join(cfg.Dir, "secrets", "peer", "key.pem")
	cfg.PeerTLSInfo.TrustedCAFile = filepath.Join(cfg.Dir, "secrets", "ca", "cert.pem")
	cfg.PeerTLSInfo.ClientCertAuth = true

	cfg.ClientTLSInfo.ServerName = "localhost"
	cfg.ClientTLSInfo.CertFile = filepath.Join(cfg.Dir, "secrets", "peer", "cert.pem")
	cfg.ClientTLSInfo.KeyFile = filepath.Join(cfg.Dir, "secrets", "peer", "key.pem")
	cfg.ClientTLSInfo.TrustedCAFile = filepath.Join(cfg.Dir, "secrets", "ca", "cert.pem")
	cfg.ClientTLSInfo.ClientCertAuth = true
	cfg.ForceNewCluster = o.ForceNewCluster

	if enableWatchCache {
		// defines the interval for etcd watch progress notify events.
		//
		// note:
		// - gcp, ocp and upstream k8s set it to 5s, so we simply follow suit
		// - in practice this value never changes so we are not exposing it as a flag/option
		// - we enable it only when the watch cache is on otherwise it might not scale
		//   sending an event every 5s to thousands of clients
		cfg.ExperimentalWatchProgressNotifyInterval = 5 * time.Second
	}

	for _, s := range o.ListenMetricsURLs {
		u, err := url.Parse(s)
		if err != nil {
			return nil, err
		}
		cfg.ListenMetricsUrls = append(cfg.ListenMetricsUrls, *u)
	}

	if enableUnsafeEtcdDisableFsyncHack, _ := strconv.ParseBool(os.Getenv("UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC")); enableUnsafeEtcdDisableFsyncHack {
		cfg.UnsafeNoFsync = true
	}

	if o.QuotaBackendBytes > 0 {
		cfg.QuotaBackendBytes = o.QuotaBackendBytes
	}

	return &Config{
		Config: cfg,
	}, nil
}

type completedConfig struct {
	*Config
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() CompletedConfig {
	return CompletedConfig{&completedConfig{
		Config: c,
	}}
}

func generateClientAndServerCerts(hosts []string, dir string) error {
	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return err
	}

	caTemplate := &x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(10 * 365 * (24 * time.Hour)),

		Subject: pkix.Name{
			Organization: []string{"etcd"},
		},

		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(0).Add(serialNumber, big.NewInt(1)),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(10 * 365 * (24 * time.Hour)),

		Subject: pkix.Name{
			Organization: []string{"etcd"},
		},

		SubjectKeyId:          []byte{1, 2, 3, 4, 6},
		DNSNames:              hosts,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(0).Add(serialNumber, big.NewInt(2)),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(10 * 365 * (24 * time.Hour)),

		Subject: pkix.Name{
			Organization: []string{"etcd"},
			CommonName:   "etcd-client",
		},

		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return err
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return err
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	if err != nil {
		return err
	}

	if err := ecPrivateKeyToFile(caKey, filepath.Join(dir, "ca", "key.pem")); err != nil {
		return err
	}
	if err := certToFile(caTemplate, caTemplate, &caKey.PublicKey, caKey, filepath.Join(dir, "ca", "cert.pem")); err != nil {
		return err
	}

	if err := ecPrivateKeyToFile(serverKey, filepath.Join(dir, "peer", "key.pem")); err != nil {
		return err
	}
	if err := certToFile(serverTemplate, caTemplate, &serverKey.PublicKey, caKey, filepath.Join(dir, "peer", "cert.pem")); err != nil {
		return err
	}

	if err := ecPrivateKeyToFile(clientKey, filepath.Join(dir, "client", "key.pem")); err != nil {
		return err
	}
	return certToFile(clientTemplate, caTemplate, &clientKey.PublicKey, caKey, filepath.Join(dir, "client", "cert.pem"))
}

func certToFile(template *x509.Certificate, parent *x509.Certificate, publicKey *ecdsa.PublicKey, privateKey *ecdsa.PrivateKey, path string) error {
	b, err := x509.CreateCertificate(rand.Reader, template, parent, publicKey, privateKey)
	if err != nil {
		return err
	}

	buf := &bytes.Buffer{}
	if err := pem.Encode(buf, &pem.Block{Type: "CERTIFICATE", Bytes: b}); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0600)
}

func ecPrivateKeyToFile(key *ecdsa.PrivateKey, path string) error {
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	buf := &bytes.Buffer{}
	if err := pem.Encode(buf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0600)
}
