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
	"strings"

	machineryutilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
)

func main() {
	// split flags into --proxy-*, --shard-* and everything elese (generic). The former are
	// passed to the respective components. Everything after "--" is considered a shard flag.
	var proxyFlags, shardFlags, genericFlags []string
	for i, arg := range os.Args[1:] {
		if arg == "--" {
			shardFlags = append(shardFlags, os.Args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "--proxy-") {
			proxyFlags = append(proxyFlags, "-"+strings.TrimPrefix(arg, "--proxy"))
		} else if strings.HasPrefix(arg, "--shard-") {
			shardFlags = append(shardFlags, "-"+strings.TrimPrefix(arg, "--shard"))
		} else {
			genericFlags = append(genericFlags, arg)
		}
	}
	flag.CommandLine.Parse(genericFlags) // nolint: errcheck

	if err := start(proxyFlags, shardFlags); err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}
}

func start(proxyFlags, shardFlags []string) error {
	ctx, cancelFn := context.WithCancel(genericapiserver.SetupSignalContext())
	defer cancelFn()

	// create requst header CA and client cert for front-proxy to connect to shards
	requestHeaderCA, err := crypto.MakeSelfSignedCA(".kcp/requestheader-ca.crt", ".kcp/requestheader-ca.key", ".kcp/requestheader-ca-serial.txt", "kcp-front-proxy-requestheader-ca", 365)
	if err != nil {
		fmt.Printf("failed to create requestheader-ca: %v\n", err)
		os.Exit(1)
	}
	_, err = requestHeaderCA.MakeClientCertificate(".kcp-front-proxy/requestheader.crt", ".kcp-front-proxy/requestheader.key", &user.DefaultInfo{Name: "kcp-front-proxy"}, 365)
	if err != nil {
		fmt.Printf("failed to create requestheader client cert: %v\n", err)
		os.Exit(1)
	}

	// create client CA and kcp-admin client cert to connect through front-proxy
	clientCA, err := crypto.MakeSelfSignedCA(".kcp/client-ca.crt", ".kcp/client-ca.key", ".kcp/client-ca-serial.txt", "kcp-client-ca", 365)
	if err != nil {
		fmt.Printf("failed to create client-ca: %v\n", err)
		os.Exit(1)
	}
	_, err = clientCA.MakeClientCertificate(".kcp/kcp-admin.crt", ".kcp/kcp-admin.key", &user.DefaultInfo{
		Name:   "kcp-admin",
		Groups: []string{"system:kcp:clusterworkspace:admin"},
	}, 365)
	if err != nil {
		fmt.Printf("failed to create kcp-admin client cert: %v\n", err)
		os.Exit(1)
	}

	// create server CA to be used to sign shard serving certs
	servingCA, err := crypto.MakeSelfSignedCA(".kcp/serving-ca.crt", ".kcp/serving-ca.key", ".kcp/serving-ca-serial.txt", "kcp-serving-ca", 365)
	if err != nil {
		fmt.Printf("failed to create requestheader-ca: %v\n", err)
		os.Exit(1)
	}

	// create service account signing and verification key
	if _, err := crypto.MakeSelfSignedCA(".kcp/service-account.crt", ".kcp/service-account.key", ".kcp/service-account-serial.txt", "kcp-service-account-signing-ca", 365); err != nil {
		fmt.Printf("failed to create service-account-signing-ca: %v\n", err)
		os.Exit(1)
	}

	// find external IP to put into certs as valid IPs
	hostIP, err := machineryutilnet.ResolveBindAddress(net.IPv4(0, 0, 0, 0))
	if err != nil {
		return err
	}

	// start root shard
	shardErrCh, err := startShard(ctx, 0, shardFlags, servingCA, hostIP.String())
	if err != nil {
		return err
	}

	// write kcp-admin kubeconfig talking to the front-proxy with a client-cert
	if err := kcpAdminKubeConfig(ctx, hostIP.String()); err != nil {
		return err
	}

	// start front-proxy
	if err := startFrontProxy(ctx, proxyFlags, servingCA, hostIP.String()); err != nil {
		return err
	}

	select {
	case <-shardErrCh:
		return fmt.Errorf("shard 0 exited: %w", err)
	case <-ctx.Done():
	}
	return nil
}
