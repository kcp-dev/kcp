/*
Copyright 2021 The KCP Authors.

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

package etcd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/server/v3/wal"

	"k8s.io/klog/v2"
)

type Server struct {
	Dir string
}

type ClientInfo struct {
	Endpoints []string
	TLS       *tls.Config

	CertFile      string
	KeyFile       string
	TrustedCAFile string
}

func (s *Server) Run(ctx context.Context, peerPort, clientPort string, walSizeBytes int64) (ClientInfo, error) {
	klog.Info("Creating embedded etcd server")
	if walSizeBytes != 0 {
		wal.SegmentSizeBytes = walSizeBytes
	}
	cfg := embed.NewConfig()

	cfg.Logger = "zap"
	cfg.LogLevel = "warn"

	cfg.Dir = s.Dir
	cfg.AuthToken = ""

	cfg.LPUrls = []url.URL{{Scheme: "https", Host: "localhost:" + peerPort}}
	cfg.APUrls = []url.URL{{Scheme: "https", Host: "localhost:" + peerPort}}
	cfg.LCUrls = []url.URL{{Scheme: "https", Host: "localhost:" + clientPort}}
	cfg.ACUrls = []url.URL{{Scheme: "https", Host: "localhost:" + clientPort}}
	cfg.InitialCluster = cfg.InitialClusterFromName(cfg.Name)

	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		return ClientInfo{}, err
	}

	if err := generateClientAndServerCerts([]string{"localhost"}, filepath.Join(cfg.Dir, "secrets")); err != nil {
		return ClientInfo{}, err
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

	if enableUnsafeEtcdDisableFsyncHack, _ := strconv.ParseBool(os.Getenv("UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC")); enableUnsafeEtcdDisableFsyncHack {
		cfg.UnsafeNoFsync = true
	}

	e, err := embed.StartEtcd(cfg)
	if err != nil {
		return ClientInfo{}, err
	}
	// Shutdown when context is closed
	go func() {
		<-ctx.Done()
		e.Close()
	}()

	clientConfig, err := cfg.ClientTLSInfo.ClientConfig()
	if err != nil {
		return ClientInfo{}, err
	}

	select {
	case <-e.Server.ReadyNotify():
		return ClientInfo{
			Endpoints:     []string{cfg.ACUrls[0].String()},
			TLS:           clientConfig,
			CertFile:      cfg.ClientTLSInfo.CertFile,
			KeyFile:       cfg.ClientTLSInfo.KeyFile,
			TrustedCAFile: cfg.ClientTLSInfo.TrustedCAFile,
		}, nil
	case <-time.After(60 * time.Second):
		e.Server.Stop() // trigger a shutdown
		return ClientInfo{}, fmt.Errorf("server took too long to start")
	case e := <-e.Err():
		return ClientInfo{}, e
	}
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
	if err := certToFile(clientTemplate, caTemplate, &clientKey.PublicKey, caKey, filepath.Join(dir, "client", "cert.pem")); err != nil {
		return err
	}

	return nil
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
	return ioutil.WriteFile(path, buf.Bytes(), 0600)
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
	return ioutil.WriteFile(path, buf.Bytes(), 0600)
}
