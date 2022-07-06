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

package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	shard "github.com/kcp-dev/kcp/cmd/test-server/kcp"
)

func startShard(ctx context.Context, n int, args []string, servingCA *crypto.CA, hostIP string) (<-chan error, error) {
	// create serving cert
	hostnames := sets.NewString("localhost", hostIP)
	klog.Infof("Creating shard server %d serving cert with hostnames %v", n, hostnames)
	cert, err := servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return nil, fmt.Errorf("failed to create server cert: %w", err)
	}
	if err := cert.WriteCertConfigFile(fmt.Sprintf(".kcp-%d/apiserver.crt", n), fmt.Sprintf(".kcp-%d/apiserver.key", n)); err != nil {
		return nil, fmt.Errorf("failed to write server cert: %w", err)
	}

	klog.Infof("Creating extra serving certs in .kcp with hostnames %v to be used by e2e webhooks", hostnames)
	cert, err = servingCA.MakeServerCert(hostnames, 365)
	if err != nil {
		return nil, fmt.Errorf("failed to create server cert: %w", err)
	}
	if err := cert.WriteCertConfigFile(".kcp/apiserver.crt", ".kcp/apiserver.key"); err != nil {
		return nil, fmt.Errorf("failed to write server cert: %w", err)
	}

	logDir := flag.Lookup("log-dir-path").Value.String()
	if err != nil {
		return nil, err
	}
	logFilePath := filepath.Join(fmt.Sprintf(".kcp-%d", n), "kcp.log")
	auditFilePath := filepath.Join(fmt.Sprintf(".kcp-%d", n), "audit.log")
	if logDir != "" {
		logFilePath = filepath.Join(logDir, fmt.Sprintf("kcp-%d.log", n))
		auditFilePath = filepath.Join(logDir, fmt.Sprintf("audit-%d.log", n))
	}

	if n > 0 {
		// args = append(args, "--root-kubeconfig=.kcp-0/root.kubeconfig")
		args = append(args, fmt.Sprintf("--embedded-etcd-client-port=%d", 2379+n+1))
		args = append(args, fmt.Sprintf("--embedded-etcd-peer-port=%d", (2379+n+1)+1)) // prev value +1
	}
	args = append(args,
		/*fmt.Sprintf("--cluster-workspace-shard-name=kcp-%d", n),*/
		fmt.Sprintf("--root-directory=.kcp-%d", n),
		"--client-ca-file=.kcp/client-ca.crt",
		"--requestheader-client-ca-file=.kcp/requestheader-ca.crt",
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		"--service-account-key-file=.kcp/service-account.crt",
		"--service-account-private-key-file=.kcp/service-account.key",
		"--audit-log-path", auditFilePath,
		fmt.Sprintf("--tls-cert-file=.kcp-%d/apiserver.crt", n),
		fmt.Sprintf("--tls-private-key-file=.kcp-%d/apiserver.key", n),
		fmt.Sprintf("--secure-port=%d", 6444+n),
	)

	return shard.Start(ctx,
		fmt.Sprintf("kcp-%d", n),  // name
		fmt.Sprintf(".kcp-%d", n), // runtime directory, etcd data etc.
		logFilePath,
		args)
}
