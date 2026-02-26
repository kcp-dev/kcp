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

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/sdk/testing/third_party/library-go/crypto"

	shard "github.com/kcp-dev/kcp/cmd/test-server/kcp"
)

func newShard(ctx context.Context, n int, args []string, standaloneVW bool, servingCA *crypto.CA, hostIP string, logDirPath, workDirPath, cacheServerConfigPath string, clientCA *crypto.CA) (*shard.Shard, error) {
	logger := klog.FromContext(ctx).WithValues("shard", n)
	kcpDir := filepath.Join(workDirPath, ".kcp")
	shardDir := filepath.Join(workDirPath, fmt.Sprintf(".kcp-%d", n))

	// create serving cert
	hostnames := sets.New("localhost", hostIP)
	logger.WithValues("hostnames", hostnames).Info("creating shard server serving cert with hostnames")
	cert, err := servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return nil, fmt.Errorf("failed to create server cert: %w", err)
	}
	if err := cert.WriteCertConfigFile(filepath.Join(shardDir, "apiserver.crt"), filepath.Join(shardDir, "apiserver.key")); err != nil {
		return nil, fmt.Errorf("failed to write server cert: %w", err)
	}

	logger.WithValues("hostnames", hostnames).Info("creating extra serving certs in .kcp with hostnames to be used by e2e webhooks")
	cert, err = servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return nil, fmt.Errorf("failed to create server cert: %w", err)
	}
	if err := cert.WriteCertConfigFile(filepath.Join(kcpDir, "apiserver.crt"), filepath.Join(kcpDir, "apiserver.key")); err != nil {
		return nil, fmt.Errorf("failed to write server cert: %w", err)
	}

	shardClientCert := filepath.Join(shardDir, "shard-client-cert.crt")
	shardClientCertKey := filepath.Join(shardDir, "shard-client-cert.key")
	shardUser := &user.DefaultInfo{Name: fmt.Sprintf("kcp-shard-%d", n), Groups: []string{"system:masters"}}
	_, err = clientCA.MakeClientCertificate(shardClientCert, shardClientCertKey, shardUser, 365)
	if err != nil {
		fmt.Printf("failed to create shard client cert: %v\n", err)
		os.Exit(1)
	}

	logFilePath := filepath.Join(shardDir, "kcp.log")
	auditFilePath := filepath.Join(shardDir, "audit.log")
	if logDirPath != "" {
		logFilePath = filepath.Join(logDirPath, fmt.Sprintf("kcp-%d.log", n))
		auditFilePath = filepath.Join(logDirPath, fmt.Sprintf("audit-%d.log", n))
	}

	if n > 0 {
		args = append(args,
			fmt.Sprintf("--shard-name=shard-%d", n),
			fmt.Sprintf("--root-shard-kubeconfig-file=%s", filepath.Join(workDirPath, ".kcp-0", "admin.kubeconfig")),
			fmt.Sprintf("--embedded-etcd-client-port=%d", embeddedEtcdClientPort(n)),
			fmt.Sprintf("--embedded-etcd-peer-port=%d", embeddedEtcdPeerPort(n)),
		)
	}
	args = append(args,
		/*fmt.Sprintf("--cluster-workspace-shard-name=kcp-%d", n),*/
		fmt.Sprintf("--root-directory=%s", filepath.Join(workDirPath, fmt.Sprintf(".kcp-%d", n))),
		fmt.Sprintf("--client-ca-file=%s", filepath.Join(kcpDir, "client-ca.crt")),
		fmt.Sprintf("--requestheader-client-ca-file=%s", filepath.Join(kcpDir, "requestheader-ca.crt")),
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		"--requestheader-extra-headers-prefix=X-Remote-Extra-",
		fmt.Sprintf("--service-account-key-file=%s", filepath.Join(kcpDir, "service-account.crt")),
		fmt.Sprintf("--service-account-private-key-file=%s", filepath.Join(kcpDir, "service-account.key")),
		fmt.Sprintf("--service-account-signing-key-file=%s", filepath.Join(kcpDir, "service-account.key")),
		// TODO(sttts): remove this flag as soon as we have service account token lookup configured.
		"--service-account-lookup=false",
		"--audit-log-path", auditFilePath,
		fmt.Sprintf("--shard-external-url=https://%s:%d", hostIP, 6443),
		fmt.Sprintf("--tls-cert-file=%s", filepath.Join(shardDir, "apiserver.crt")),
		fmt.Sprintf("--tls-private-key-file=%s", filepath.Join(shardDir, "apiserver.key")),
		fmt.Sprintf("--secure-port=%d", 6444+n),
		fmt.Sprintf("--logical-cluster-admin-kubeconfig=%s", filepath.Join(kcpDir, "logical-cluster-admin.kubeconfig")),
		fmt.Sprintf("--external-logical-cluster-admin-kubeconfig=%s", filepath.Join(kcpDir, "external-logical-cluster-admin.kubeconfig")),
		fmt.Sprintf("--shard-client-cert-file=%s", shardClientCert),
		fmt.Sprintf("--shard-client-key-file=%s", shardClientCertKey),
		fmt.Sprintf("--shard-virtual-workspace-ca-file=%s", filepath.Join(kcpDir, "serving-ca.crt")),
		"--service-account-lookup=false",
	)
	if len(cacheServerConfigPath) > 0 {
		args = append(args, fmt.Sprintf("--cache-kubeconfig=%s", cacheServerConfigPath))
	}

	if standaloneVW {
		args = append(args, fmt.Sprintf("--shard-virtual-workspace-url=https://%s",
			net.JoinHostPort(hostIP, virtualWorkspacePort(n))))
	}

	return shard.NewShard(
		fmt.Sprintf("kcp-%d", n),                              // name
		filepath.Join(workDirPath, fmt.Sprintf(".kcp-%d", n)), // runtime directory, etcd data etc.
		logFilePath,
		args,
	), nil
}

func virtualWorkspacePort(n int) string {
	return strconv.Itoa(7444 + n)
}

func embeddedEtcdClientPort(n int) int {
	return 2380 + (n * 2) - 1
}

func embeddedEtcdPeerPort(n int) int {
	return 2380 + (n * 2)
}
