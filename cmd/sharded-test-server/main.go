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

	machineryutilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	shard "github.com/kcp-dev/kcp/cmd/test-server/kcp"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func main() {
	logDirPath := flag.String("log-dir-path", "", "Path to the log files. If empty, log files are stored in the dot directories.")
	workDirPath := flag.String("work-dir-path", "", "Path to the working directory where the .kcp* dot directories are created. If empty, the working directory is the current directory.")
	numberOfShards := flag.Int("number-of-shards", 1, "The number of shards to create. The first created is assumed root.")

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

	if err := start(proxyFlags, shardFlags, *logDirPath, *workDirPath, *numberOfShards); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func start(proxyFlags, shardFlags []string, logDirPath, workDirPath string, numberOfShards int) error {
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
		fmt.Printf("failed to create requestheader-ca: %v\n", err)
		os.Exit(1)
	}
	_, err = requestHeaderCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.crt"),
		filepath.Join(workDirPath, ".kcp-front-proxy/requestheader.key"),
		&user.DefaultInfo{Name: "kcp-front-proxy"},
		365,
	)
	if err != nil {
		fmt.Printf("failed to create requestheader client cert: %v\n", err)
		os.Exit(1)
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
		fmt.Printf("failed to create client-ca: %v\n", err)
		os.Exit(1)
	}
	_, err = clientCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp/kcp-admin.crt"),
		filepath.Join(workDirPath, ".kcp/kcp-admin.key"),
		&user.DefaultInfo{
			Name:   "kcp-admin",
			Groups: []string{bootstrap.SystemKcpAdminGroup},
		},
		365,
	)
	if err != nil {
		fmt.Printf("failed to create kcp-admin client cert: %v\n", err)
		os.Exit(1)
	}

	// TODO:(p0lyn0mial): in the future we need a separate group valid only for the proxy
	// so that it can make wildcard requests against shards
	// for now we will use the privileged system:masters group to bypass the authz stack
	// create system:masters client cert to connect to shards
	_, err = clientCA.MakeClientCertificate(
		filepath.Join(workDirPath, ".kcp-front-proxy/shard-admin.crt"),
		filepath.Join(workDirPath, ".kcp-front-proxy/shard-admin.key"),
		&user.DefaultInfo{
			Name:   "shard-admin",
			Groups: []string{"system:masters"},
		},
		365,
	)
	if err != nil {
		fmt.Printf("failed to create front proxy shard admin client cert: %v\n", err)
		os.Exit(1)
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
		fmt.Printf("failed to create serving-ca: %v\n", err)
		os.Exit(1)
	}

	// create service account signing and verification key
	if _, err := crypto.MakeSelfSignedCA(
		filepath.Join(workDirPath, ".kcp/service-account.crt"),
		filepath.Join(workDirPath, ".kcp/service-account.key"),
		filepath.Join(workDirPath, ".kcp/service-account-serial.txt"),
		"kcp-service-account-signing-ca",
		365,
	); err != nil {
		fmt.Printf("failed to create service-account-signing-ca: %v\n", err)
		os.Exit(1)
	}

	// find external IP to put into certs as valid IPs
	hostIP, err := machineryutilnet.ResolveBindAddress(net.IPv4(0, 0, 0, 0))
	if err != nil {
		return err
	}

	standaloneVW := sets.NewString(shardFlags...).Has("--run-virtual-workspaces=false")
	if standaloneVW {
		shardFlags = append(shardFlags, fmt.Sprintf("--shard-virtual-workspace-url=https://%s:7444", hostIP))
	}

	cacheServerErrCh := make(chan indexErrTuple)
	cacheServerConfigPath := ""
	if sets.NewString(shardFlags...).Has("--run-cache-server=true") {
		cacheServerCh, configPath, err := startCacheServer(ctx, logDirPath, workDirPath)
		if err != nil {
			return fmt.Errorf("error starting the cache server: %w", err)
		}
		cacheServerConfigPath = configPath
		go func() {
			err := <-cacheServerCh
			cacheServerErrCh <- indexErrTuple{0, err}
		}()
	}

	// start shards
	var shards []*shard.Shard
	for i := 0; i < numberOfShards; i++ {
		shard, err := newShard(ctx, i, shardFlags, servingCA, hostIP.String(), logDirPath, workDirPath, cacheServerConfigPath)
		if err != nil {
			return err
		}
		if err := shard.Start(ctx); err != nil {
			return err
		}
		shards = append(shards, shard)
	}

	vwPort := "6444"
	virtualWorkspacesErrCh := make(chan indexErrTuple)
	if standaloneVW {
		// TODO: support multiple virtual workspace servers (i.e. multiple ports)
		vwPort = "7444"

		for i := 0; i < numberOfShards; i++ {
			virtualWorkspaceErrCh, err := startVirtual(ctx, i, logDirPath, workDirPath)
			if err != nil {
				return fmt.Errorf("error starting virtual workspaces server %d: %w", i, err)
			}
			go func(vwIndex int, vwErrCh <-chan error) {
				err := <-virtualWorkspaceErrCh
				virtualWorkspacesErrCh <- indexErrTuple{vwIndex, err}
			}(i, virtualWorkspaceErrCh)
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
	if err := startFrontProxy(ctx, proxyFlags, servingCA, hostIP.String(), logDirPath, workDirPath, vwPort); err != nil {
		return err
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
