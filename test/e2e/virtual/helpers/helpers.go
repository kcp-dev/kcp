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

package helpers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/rest"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	virtualcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/cluster/client/clientset/versioned/scheme"
)

// TODO: get rid of this package and move back into virtual_workspace_test.go

type VirtualWorkspaceClientContext struct {
	Prefix string
	User   framework.User
}

type VirtualWorkspace struct {
	BuildSubCommandOptions func(kcpServer framework.RunningServer) virtualcmd.SubCommandOptions
	ClientContexts         []VirtualWorkspaceClientContext
}

func (vw VirtualWorkspace) Setup(t *testing.T, ctx context.Context, kcpServer framework.RunningServer, orgClusterName string) ([]*rest.Config, error) {
	kcpKubeconfigPath := kcpServer.KubeconfigPath()
	kcpDataDir := filepath.Dir(kcpKubeconfigPath)

	secureOptions := kubeoptions.NewSecureServingOptions()
	secureOptions.ServerCert.CertKey.CertFile = filepath.Join(kcpDataDir, "apiserver.crt")
	secureOptions.ServerCert.CertKey.KeyFile = filepath.Join(kcpDataDir, "apiserver.key")

	portStr, err := framework.GetFreePort(t)
	if err != nil {
		return nil, err
	}
	bindPort, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	secureOptions.BindPort = bindPort
	externalAddress, err := secureOptions.DefaultExternalAddress()
	if err != nil {
		return nil, err
	}
	authenticationOptions := options.NewDelegatingAuthenticationOptions()
	authenticationOptions.RemoteKubeConfigFile = kcpKubeconfigPath
	authenticationOptions.SkipInClusterLookup = true
	vwOptions := virtualcmd.APIServerOptions{
		Output:            os.Stdout,
		SecureServing:     secureOptions,
		Authentication:    authenticationOptions,
		SubCommandOptions: vw.BuildSubCommandOptions(kcpServer),
	}

	virtualWorkspaceContext, stopVirtualWorkspace := context.WithCancel(ctx)
	virtualWorkspaceStopped := make(chan struct{}, 1)
	t.Cleanup(func() {
		stopVirtualWorkspace()
		<-virtualWorkspaceStopped
	})

	go func() {
		t.Log("Running the virtualWorkspace")
		err = vwOptions.RunAPIServer(virtualWorkspaceContext.Done())
		t.Log("VirtualWorkspace stopped")
		if err != nil && ctx.Err() == nil {
			// we care about errors in the process that did not result from the
			// context expiring and us ending the process
			t.Errorf("`virtual-workspaces` failed: %v", err)
		}
		virtualWorkspaceStopped <- struct{}{}
	}()

	mainRestConfig := &rest.Config{
		Host:    fmt.Sprintf("%s:%d", externalAddress, bindPort),
		APIPath: "/apis",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
	}

	vwReadinessClient, err := rest.UnversionedRESTClientFor(mainRestConfig)
	if err != nil {
		return nil, err
	}

	var succeeded bool
	var message string
	loadCtx, cancel := context.WithTimeout(virtualWorkspaceContext, 1*time.Minute)
	wait.UntilWithContext(loadCtx, func(ctx context.Context) {
		endpoint := "/readyz"
		_, err := rest.NewRequest(vwReadinessClient).RequestURI(endpoint).Do(ctx).Raw()
		if err == nil {
			t.Logf("success contacting %s", endpoint)
			cancel()
			succeeded = true
		} else {
			message = fmt.Sprintf("error contacting %s: %v", endpoint, err)
		}
	}, 100*time.Millisecond)
	if !succeeded {
		return nil, errors.New(message)
	}

	virtualWorkspaceConfigs := []*rest.Config{}
	kcpRawConfig, err := kcpServer.RawConfig()
	if err != nil {
		return nil, err
	}
	for _, vwClientContext := range vw.ClientContexts {
		vwCfg := rest.CopyConfig(mainRestConfig)
		vwCfg.Host = "https://" + vwCfg.Host + "/" + orgClusterName + vwClientContext.Prefix
		if authInfo, exists := kcpRawConfig.AuthInfos[vwClientContext.User.Name]; exists && vwClientContext.User.Token == "" {
			vwCfg.BearerToken = authInfo.Token
		} else {
			vwCfg.BearerToken = vwClientContext.User.Token
		}
		virtualWorkspaceConfigs = append(virtualWorkspaceConfigs, vwCfg)
	}
	return virtualWorkspaceConfigs, nil
}
