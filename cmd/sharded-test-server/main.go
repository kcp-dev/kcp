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
	"net"
	"os"
	"path/filepath"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	machineryutilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/sets"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	shard "github.com/kcp-dev/kcp/cmd/test-server/kcp"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
)

func main() {
	logDirPath := flag.String("log-dir-path", "", "Path to the log files. If empty, log files are stored in the dot directories.")
	workDirPath := flag.String("work-dir-path", "", "Path to the working directory where the .kcp* dot directories are created. If empty, the working directory is the current directory.")
	numberOfShards := flag.Int("number-of-shards", 1, "The number of shards to create. The first created is assumed root.")
	quiet := flag.Bool("quiet", false, "Suppress output of the subprocesses")

	// split flags into --proxy-*, --shard-* and everything else (generic). The former are
	// passed to the respective components.
	var proxyFlags, shardFlags, genericFlags []string
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--proxy-") {
			proxyFlags = append(proxyFlags, "-"+strings.TrimPrefix(arg, "--proxy"))
		} else if strings.HasPrefix(arg, "--shard-") {
			shardFlags = append(shardFlags, "-"+strings.TrimPrefix(arg, "--shard"))
		} else {
			genericFlags = append(genericFlags, arg)
		}
	}
	flag.CommandLine.Parse(genericFlags) //nolint:errcheck

	if err := start(proxyFlags, shardFlags, *logDirPath, *workDirPath, *numberOfShards, *quiet); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func start(proxyFlags, shardFlags []string, logDirPath, workDirPath string, numberOfShards int, quiet bool) error {
	ctx, cancelFn := context.WithCancel(genericapiserver.SetupSignalContext())
	defer cancelFn()

	// create request header CA and client cert for front-proxy to connect to shards
	requestHeaderCA, err := crypto.MakeSelfSignedCA(
		filepath.Join(workDirPath, ".kcp/requestheader-ca.crt"),
		filepath.Join(workDirPath, ".kcp/requestheader-ca.key"),
		filepath.Join(workDirPath, ".kcp/requestheader-ca-serial.txt"),
		"kcp-front-proxy-requestheader-ca",
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create requestheader-ca: %w", err)
	}
	_, err = requestHeaderCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.crt"),
		filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.key"),
		&kuser.DefaultInfo{Name: "kcp-front-proxy"},
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create requestheader client cert: %w", err)
	}

	// create client CA and kcp-admin client cert to connect through front-proxy
	clientCA, err := crypto.MakeSelfSignedCA(
		filepath.Join(workDirPath, ".kcp/client-ca.crt"),
		filepath.Join(workDirPath, ".kcp/client-ca.key"),
		filepath.Join(workDirPath, ".kcp/client-ca-serial.txt"),
		"kcp-client-ca",
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create client-ca: %w", err)
	}
	_, err = clientCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp/kcp-admin.crt"),
		filepath.Join(workDirPath, ".kcp/kcp-admin.key"),
		&kuser.DefaultInfo{
			Name:   "kcp-admin",
			Groups: []string{bootstrap.SystemKcpAdminGroup},
		},
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create kcp-admin client cert: %w", err)
	}

	// client cert for logical-cluster-admin
	_, err = clientCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp/logical-cluster-admin.crt"),
		filepath.Join(workDirPath, ".kcp/logical-cluster-admin.key"),
		&kuser.DefaultInfo{
			Name:   "logical-cluster-admin",
			Groups: []string{bootstrap.SystemLogicalClusterAdmin},
		},
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create kcp-logical-cluster client cert: %w", err)
	}

	// TODO:(p0lyn0mial): in the future we need a separate group valid only for the proxy
	// so that it can make wildcard requests against shards
	// for now we will use the privileged system group to bypass the authz stack
	// create privileged system user client cert to connect to shards
	_, err = clientCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp-front-proxy/shard-admin.crt"),
		filepath.Join(workDirPath, ".kcp-front-proxy/shard-admin.key"),
		&kuser.DefaultInfo{
			Name:   "shard-admin",
			Groups: []string{kuser.SystemPrivilegedGroup},
		},
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create front proxy shard admin client cert: %w", err)
	}

	// create server CA to be used to sign shard serving certs
	servingCA, err := crypto.MakeSelfSignedCA(
		filepath.Join(workDirPath, ".kcp/serving-ca.crt"),
		filepath.Join(workDirPath, ".kcp/serving-ca.key"),
		filepath.Join(workDirPath, ".kcp/serving-ca-serial.txt"),
		"kcp-serving-ca",
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create serving-ca: %w", err)
	}

	// create service account signing and verification key
	if _, err := crypto.MakeSelfSignedCA(
		filepath.Join(workDirPath, ".kcp/service-account.crt"),
		filepath.Join(workDirPath, ".kcp/service-account.key"),
		filepath.Join(workDirPath, ".kcp/service-account-serial.txt"),
		"kcp-service-account-signing-ca",
		365,
	); err != nil {
		return fmt.Errorf("failed to create service-account-signing-ca: %w", err)
	}

	// find external IP to put into certs as valid IPs
	hostIP, err := machineryutilnet.ResolveBindAddress(net.IPv4(0, 0, 0, 0))
	if err != nil {
		return err
	}

	standaloneVW := sets.NewString(shardFlags...).Has("--run-virtual-workspaces=false")

	cacheServerErrCh := make(chan indexErrTuple)
	cacheServerConfigPath := ""
	cacheServerCh, configPath, err := startCacheServer(ctx, logDirPath, workDirPath)
	if err != nil {
		return fmt.Errorf("error starting the cache server: %w", err)
	}
	cacheServerConfigPath = configPath
	go func() {
		err := <-cacheServerCh
		cacheServerErrCh <- indexErrTuple{0, err}
	}()

	if err := writeLogicalClusterAdminKubeConfig(hostIP.String(), workDirPath); err != nil {
		return err
	}

	// start shards
	var shards []*shard.Shard
	for i := 0; i < numberOfShards; i++ {
		shard, err := newShard(ctx, i, shardFlags, standaloneVW, servingCA, hostIP.String(), logDirPath, workDirPath, cacheServerConfigPath, clientCA)
		if err != nil {
			return err
		}
		if err := shard.Start(ctx, quiet); err != nil {
			return err
		}
		shards = append(shards, shard)
	}

	// Start virtual-workspace servers
	vwPort := "6444"
	var virtualWorkspaces []*VirtualWorkspace
	if standaloneVW {
		// TODO: support multiple virtual workspace servers (i.e. multiple ports)
		vwPort = "7444"

		for i := 0; i < numberOfShards; i++ {
			vw, err := newVirtualWorkspace(ctx, i, servingCA, hostIP.String(), logDirPath, workDirPath, clientCA)
			if err != nil {
				return err
			}
			if err := vw.start(ctx); err != nil {
				return err
			}
			virtualWorkspaces = append(virtualWorkspaces, vw)
		}
	}

	// Wait for shards to be ready
	shardsErrCh := make(chan indexErrTuple)
	for i, shard := range shards {
		terminatedCh, err := shard.WaitForReady(ctx)
		if err != nil {
			return err
		}
		go func(i int, terminatedCh <-chan error) {
			err := <-terminatedCh
			shardsErrCh <- indexErrTuple{i, err}
		}(i, terminatedCh)
	}

	// Wait for virtual workspaces to be ready
	virtualWorkspacesErrCh := make(chan indexErrTuple)
	if standaloneVW {
		for i, vw := range virtualWorkspaces {
			terminatedCh, err := vw.waitForReady(ctx)
			if err != nil {
				return err
			}
			go func(i int, terminatedCh <-chan error) {
				err := <-terminatedCh
				virtualWorkspacesErrCh <- indexErrTuple{i, err}
			}(i, terminatedCh)
		}
	}

	// write kcp-admin kubeconfig talking to the front-proxy with a client-cert
	if err := writeAdminKubeConfig(hostIP.String(), workDirPath); err != nil {
		return err
	}
	// this is system-master kubeconfig used by the front-proxy to talk to shards
	if err := writeShardKubeConfig(workDirPath); err != nil {
		return err
	}

	// start front-proxy
	if err := startFrontProxy(ctx, proxyFlags, servingCA, hostIP.String(), logDirPath, workDirPath, vwPort, quiet); err != nil {
		return err
	}

	// Label region of shards
	clientConfig, err := loadKubeConfig(filepath.Join(workDirPath, ".kcp/admin.kubeconfig"), "base")
	if err != nil {
		return err
	}
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return err
	}
	client, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return err
	}
	for i := range shards {
		name := fmt.Sprintf("shard-%d", i)
		if i == 0 {
			name = "root"
		}

		if i >= len(regions) {
			break
		}
		patch := fmt.Sprintf(`{"metadata":{"labels":{"region":%q}}}`, regions[i])
		if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			_, err := client.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Patch(ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
			return err
		}); err != nil {
			return err
		}
	}

	select {
	case shardIndexErr := <-shardsErrCh:
		return fmt.Errorf("shard %d exited: %w", shardIndexErr.index, shardIndexErr.error)
	case vwIndexErr := <-virtualWorkspacesErrCh:
		return fmt.Errorf("virtual workspaces %d exited: %w", vwIndexErr.index, vwIndexErr.error)
	case cacheErr := <-cacheServerErrCh:
		return fmt.Errorf("cache server exited: %w", cacheErr.error)
	case <-ctx.Done():
	}
	return nil
}

type indexErrTuple struct {
	index int
	error error
}

func loadKubeConfig(kubeconfigPath, contextName string) (clientcmd.ClientConfig, error) {
	fs, err := os.Stat(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	if fs.Size() == 0 {
		return nil, fmt.Errorf("%s points to an empty file", kubeconfigPath)
	}

	rawConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load admin kubeconfig: %w", err)
	}

	return clientcmd.NewNonInteractiveClientConfig(*rawConfig, contextName, nil, nil), nil
}

var regions = []string{
	"us-east-2",
	"us-east-1",
	"us-west-1",
	"us-west-2",
	"af-south-1",
	"ap-east-1",
	"ap-south-2",
	"ap-southeast-3",
	"ap-south-1",
	"ap-northeast-3",
	"ap-northeast-2",
	"ap-southeast-1",
	"ap-southeast-2",
	"ap-northeast-1",
	"ca-central-1",
	"eu-central-1",
	"eu-west-1",
	"eu-west-2",
	"eu-south-1",
	"eu-west-3",
	"eu-south-2",
	"eu-north-1",
	"eu-central-2",
	"me-south-1",
	"me-central-1",
	"sa-east-1",
}
