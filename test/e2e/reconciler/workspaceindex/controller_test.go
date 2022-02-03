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

package workspaceindex

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/config"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	utilconditions "github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type runningServer struct {
	framework.RunningServer
	client          clientset.Interface
	kubeClient      kubernetesclientset.Interface
	expectWorkspace framework.RegisterWorkspaceExpectation
	expectShard     framework.RegisterWorkspaceShardExpectation
}

func resolveRunningServer(ctx context.Context, t framework.TestingTInterface, server framework.RunningServer, clusterName string) (runningServer, error) {
	cfg, err := server.Config()
	if err != nil {
		return runningServer{}, err
	}
	if clusterName == "" {
		detectedName, err := framework.DetectClusterName(cfg, ctx, "workspaces.tenancy.kcp.dev")
		if err != nil {
			return runningServer{}, fmt.Errorf("failed to detect cluster name: %w", err)
		}
		clusterName = detectedName
	} else {
		extensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
		if err != nil {
			return runningServer{}, fmt.Errorf("failed to construct extensions client for server: %w", err)
		}
		extensionsClient := extensionsClients.Cluster(clusterName)
		requiredCrds := []metav1.GroupResource{
			{Group: tenancyapi.GroupName, Resource: "workspaces"},
			{Group: tenancyapi.GroupName, Resource: "workspaceshards"},
		}
		crdClient := extensionsClient.ApiextensionsV1().CustomResourceDefinitions()
		if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, requiredCrds); err != nil {
			return runningServer{}, err
		}
	}
	clients, err := kcpclientset.NewClusterForConfig(cfg)
	if err != nil {
		return runningServer{}, fmt.Errorf("failed to construct client for server: %w", err)
	}
	client := clients.Cluster(clusterName)
	expectWorkspace, err := framework.ExpectWorkspaces(ctx, t, client)
	if err != nil {
		return runningServer{}, fmt.Errorf("failed to start expecter: %w", err)
	}
	expectShard, err := framework.ExpectWorkspaceShards(ctx, t, client)
	if err != nil {
		return runningServer{}, fmt.Errorf("failed to start expecter: %w", err)
	}
	kubeClients, err := kubernetesclientset.NewClusterForConfig(cfg)
	if err != nil {
		return runningServer{}, fmt.Errorf("failed to construct kube client for server: %w", err)
	}
	kubeClient := kubeClients.Cluster(clusterName)
	return runningServer{
		RunningServer:   server,
		client:          client,
		kubeClient:      kubeClient,
		expectWorkspace: expectWorkspace,
		expectShard:     expectShard,
	}, nil
}

func TestWorkspaceIndex(t *testing.T) {
	var testCases = []struct {
		name string
		work func(ctx context.Context, t framework.TestingTInterface, port string, observed map[string]map[string]string)
	}{
		{
			name: "test",
			work: func(ctx context.Context, t framework.TestingTInterface, port string, observed map[string]map[string]string) {
				resolve := func(logicalCluster, resourceVersion string) (string, error) {
					u, err := url.Parse(fmt.Sprintf("http://[::1]:%s/shard", port))
					if err != nil {
						return "", err
					}
					q := u.Query()
					q.Set("clusterName", logicalCluster)
					q.Set("resourceVersion", resourceVersion)
					u.RawQuery = q.Encode()
					resp, err := http.Get(u.String())
					if err != nil {
						return "", err
					}
					if resp.StatusCode != http.StatusOK {
						return "", fmt.Errorf("request did not get 200, but %d", resp.StatusCode)
					}
					body, err := ioutil.ReadAll(resp.Body)
					if err != nil {
						return "", err
					}
					if err := resp.Body.Close(); err != nil {
						return "", err
					}
					t.Logf("resolved %s@%s to %s", logicalCluster, resourceVersion, string(body))
					return string(body), nil
				}

				resp, err := http.Get(fmt.Sprintf("http://[::1]:%s/data", port))
				if err != nil {
					t.Errorf("failed to get data from proxy: %v", err)
					return
				}
				if resp.StatusCode != http.StatusOK {
					t.Errorf("request for data did not get 200, but %d", resp.StatusCode)
					return
				}
				body, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					t.Errorf("failed to read data from body: %v", err)
					return
				}
				if err := resp.Body.Close(); err != nil {
					t.Errorf("failed to close body: %v", err)
					return
				}
				t.Log(string(body))

				for orgName, workspaces := range observed {
					for workspaceName, shardName := range workspaces {
						logicalCluster := orgName + "_" + workspaceName
						shard, err := resolve(logicalCluster, "")
						if err != nil {
							t.Errorf("%s/%s: expected no error but got one: %v", orgName, workspaceName, err)
							return
						}
						if shard != shardName {
							t.Errorf("%s/%s: expected %s on %s, got %s", orgName, workspaceName, logicalCluster, shardName, shard)
							return
						}
					}
				}
			},
		},
	}
	const (
		// serverNameMain will hold the root workspace
		serverNameMain = "main"
		// serverName* will hold org workspaces
		serverNameEast    = "east"
		serverNameCentral = "central"
		serverNameWest    = "west"
		// orgName* are the organizations
		orgNameAcme    = "acme"
		orgNameInitech = "initech"
		orgNameGlobex  = "globex"
	)
	for i := range testCases {
		testCase := testCases[i]
		framework.RunParallel(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}
			if len(servers) != 4 {
				t.Errorf("incorrect number of servers: %d", len(servers))
				return
			}
			mainServer, err := resolveRunningServer(ctx, t, servers[serverNameMain], "")
			if err != nil {
				t.Error(err)
				return
			}
			// in the root workspace, set up:
			// - WorkspaceShard objects with the credentials for our servers
			// - Workspace objects for our organizations
			// in the "admin" workspace on each shard, set up:
			// - WorkspaceShard object for the scheduler (this will be removed when we have a cross-shard client)
			// - Workspace objects (these are end-user-facing)
			serverNames := []string{serverNameEast, serverNameCentral, serverNameWest}
			orgs := []string{orgNameAcme, orgNameInitech, orgNameGlobex}
			workspaces := []string{"accounting", "engineering", "executive"}
			initErrorChan := make(chan error, len(serverNames)+len(orgs))
			wg := sync.WaitGroup{}
			wg.Add(len(serverNames))
			initStart := time.Now()
			t.Log("bootstrapping WorkspaceShards in root logical cluster")
			for _, serverName := range serverNames {
				go func(serverName string) {
					defer wg.Done()

					rawCfg, err := servers[serverName].RawConfig()
					if err != nil {
						initErrorChan <- fmt.Errorf("failed to resolve raw credentials: %w", err)
						return
					}
					if err := initializeShard(ctx, t, mainServer, serverName, rawCfg); err != nil {
						initErrorChan <- err
						return
					}
				}(serverName)
			}
			wg.Wait()
			close(initErrorChan)
			t.Logf("finished bootstrapping WorkspaceShards in root logical cluster in %s", time.Since(initStart))
			var initErrors []error
			for err := range initErrorChan {
				if err != nil {
					initErrors = append(initErrors, err)
				}
			}
			if len(initErrors) > 0 {
				t.Errorf("failed to bootstrap shards: %v", kerrors.NewAggregate(initErrors))
				return
			}
			mapping := map[string]map[string]string{} // org to workspace to shard
			mappingLock := sync.Mutex{}
			orgErrorChan := make(chan error, len(orgs))
			wg.Add(len(orgs))
			orgInitStart := time.Now()
			t.Log("bootstrapping organizational Workspaces in root logical cluster and their data")
			for _, orgName := range orgs {
				go func(orgName string) {
					defer wg.Done()
					orgWorkspace, err := initializeWorkspace(ctx, t, mainServer, orgName)
					if err != nil {
						orgErrorChan <- err
						return
					}
					mappingLock.Lock()
					if _, recorded := mapping[helper.OrganizationCluster]; !recorded {
						mapping[helper.OrganizationCluster] = map[string]string{}
					}
					mapping[helper.OrganizationCluster][orgName] = orgWorkspace.Status.Location.Current
					mappingLock.Unlock()

					clusterName, err := helper.EncodeLogicalClusterName(orgWorkspace)
					if err != nil {
						orgErrorChan <- err
						return
					}
					shardServer, err := resolveRunningServer(ctx, t, servers[orgWorkspace.Status.Location.Current], clusterName)
					if err != nil {
						orgErrorChan <- err
						return
					}

					// TODO: when we have a cross-shard workspace scheduler in the root shard, we don't need this shard object in the delegate kcp shard
					if err := initializeShard(ctx, t, shardServer, orgName+"-delegate", clientcmdapi.Config{
						Clusters:       map[string]*clientcmdapi.Cluster{"cluster": {Server: "https://kcp.dev/apiprefix"}},
						Contexts:       map[string]*clientcmdapi.Context{"context": {Cluster: "cluster", AuthInfo: "user"}},
						CurrentContext: "context",
						AuthInfos:      map[string]*clientcmdapi.AuthInfo{"user": {Username: "user", Password: "password"}},
					}); err != nil {
						orgErrorChan <- err
						return
					}

					for _, workspaceName := range workspaces {
						workspace, err := initializeWorkspace(ctx, t, shardServer, workspaceName)
						if err != nil {
							orgErrorChan <- err
							return
						}
						mappingLock.Lock()
						if _, recorded := mapping[orgName]; !recorded {
							mapping[orgName] = map[string]string{}
						}
						mapping[orgName][workspaceName] = workspace.Status.Location.Current
						mappingLock.Unlock()
					}
				}(orgName)
			}
			wg.Wait()
			t.Logf("finished bootstrapping organizational Workspaces in root logical cluster and their data in %s", time.Since(orgInitStart))
			close(orgErrorChan)
			var orgErrors []error
			for err := range orgErrorChan {
				if err != nil {
					orgErrors = append(orgErrors, err)
				}
			}
			if len(orgErrors) > 0 {
				t.Errorf("failed to bootstrap org workspaces in root workspace: %v", kerrors.NewAggregate(orgErrors))
				return
			}
			port, err := framework.GetFreePort(t)
			if err != nil {
				t.Error(err)
				return
			}
			cfg, err := servers[serverNameMain].RawConfig()
			if err != nil {
				t.Error(err)
				return
			}
			proxy := framework.NewAccessory(t, artifactDir,
				"shard-proxy",
				"--port="+port,
				"--root-kubeconfig="+cfg.Clusters[cfg.CurrentContext].LocationOfOrigin,
			)
			go func() {
				if err := proxy.Run(ctx); err != nil {
					t.Error(err)
				}
			}()
			if !framework.Ready(ctx, t, port) {
				t.Log("failed to wait for accessory to be ready")
				return
			}
			mappingLock.Lock()
			defer mappingLock.Unlock()
			t.Logf("expecting: %#v", mapping)
			testCase.work(ctx, t, port, mapping)
		}, framework.KcpConfig{
			Name: serverNameMain,
			Args: []string{"--run-controllers=false", "--unsupported-run-individual-controllers=workspace-scheduler"},
		}, framework.KcpConfig{
			Name: serverNameEast,
			Args: []string{"--run-controllers=false", "--unsupported-run-individual-controllers=workspace-scheduler"}, // TODO: when we have the shard proxy working for API requests, we only need to run the workspace-related controllers in the main shard
		}, framework.KcpConfig{
			Name: serverNameCentral,
			Args: []string{"--run-controllers=false", "--unsupported-run-individual-controllers=workspace-scheduler"},
		}, framework.KcpConfig{
			Name: serverNameWest,
			Args: []string{"--run-controllers=false", "--unsupported-run-individual-controllers=workspace-scheduler"},
		})
	}
}

func isUnschedulable(workspace *tenancyv1alpha1.Workspace) bool {
	return utilconditions.IsFalse(workspace, tenancyv1alpha1.WorkspaceScheduled) && utilconditions.GetReason(workspace, tenancyv1alpha1.WorkspaceScheduled) == tenancyv1alpha1.WorkspaceReasonUnschedulable
}

func scheduledAnywhere(object *tenancyv1alpha1.Workspace) error {
	if isUnschedulable(object) {
		return fmt.Errorf("expected a scheduled workspace, got status.conditions: %#v", object.Status.Conditions)
	}
	if object.Status.Location.Current == "" {
		return fmt.Errorf("expected workspace.status.location.current to be anything, got %q", object.Status.Location.Current)
	}
	return nil
}

func initializeShard(ctx context.Context, t framework.TestingTInterface, server runningServer, serverName string, rawCfg clientcmdapi.Config) error {
	if _, err := server.kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "credentials"}}, metav1.CreateOptions{}); !errors.IsAlreadyExists(err) && err != nil {
		return fmt.Errorf("failed to create credentials namespace: %w", err)
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return server.kubeClient.CoreV1().Namespaces().Get(ctx, "credentials", metav1.GetOptions{})
	})

	rawBytes, err := clientcmd.Write(rawCfg)
	if err != nil {
		return fmt.Errorf("could not serialize raw config: %w", err)
	}
	if _, err := server.kubeClient.CoreV1().Secrets("credentials").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: serverName},
		Data: map[string][]byte{
			"kubeconfig": rawBytes,
		},
	}, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create credentials secret: %w", err)
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return server.kubeClient.CoreV1().Secrets("credentials").Get(ctx, serverName, metav1.GetOptions{})
	})
	workspaceShard, err := server.client.TenancyV1alpha1().WorkspaceShards().Create(ctx, &tenancyv1alpha1.WorkspaceShard{
		ObjectMeta: metav1.ObjectMeta{Name: serverName},
		Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
			Name:      serverName,
			Namespace: "credentials",
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create workspace shard: %w", err)
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return server.client.TenancyV1alpha1().WorkspaceShards().Get(ctx, serverName, metav1.GetOptions{})
	})
	if err := server.expectShard(workspaceShard, func(shard *tenancyv1alpha1.WorkspaceShard) error {
		if !utilconditions.IsTrue(shard, tenancyv1alpha1.WorkspaceShardCredentialsValid) {
			return fmt.Errorf("workspace shard %s does not have valid credentials, conditions: %#v", shard.Name, shard.GetConditions())
		}
		return nil
	}); err != nil {
		return fmt.Errorf("did not see workspace shard get valid credentials: %w", err)
	}
	return nil
}

func initializeWorkspace(ctx context.Context, t framework.TestingTInterface, server runningServer, name string) (*tenancyv1alpha1.Workspace, error) {
	orgWorkspace, err := server.client.TenancyV1alpha1().Workspaces().Create(ctx, &tenancyv1alpha1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}
	server.Artifact(t, func() (runtime.Object, error) {
		return server.client.TenancyV1alpha1().Workspaces().Get(ctx, name, metav1.GetOptions{})
	})
	var ws *tenancyv1alpha1.Workspace
	if err := server.expectWorkspace(orgWorkspace, func(workspace *tenancyv1alpha1.Workspace) error {
		if err := scheduledAnywhere(workspace); err != nil {
			return err
		}
		if !utilconditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceURLValid) {
			return fmt.Errorf("expected valid URL on workspace, got: %v", utilconditions.Get(workspace, tenancyv1alpha1.WorkspaceURLValid))
		}
		ws = workspace
		return nil
	}); err != nil {
		return nil, fmt.Errorf("did not see workspace updated: %w", err)
	}
	return ws, nil
}
