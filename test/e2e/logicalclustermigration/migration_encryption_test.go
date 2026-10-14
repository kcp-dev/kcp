/*
Copyright 2026 The kcp Authors.

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

package logicalclustermigration

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMigrationEncryptionAtRest(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	for _, mode := range []string{"aesgcm", "aescbc"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			args := map[string][]string{}
			for i, name := range []string{"root", "shard-1"} {
				// Each shard has a different key as well as a different storage prefix.
				key := make([]byte, 32)
				for n := range key {
					key[n] = byte(i + 1)
				}
				path := filepath.Join(t.TempDir(), "encryption.yaml")
				config := fmt.Sprintf(`apiVersion: apiserver.config.k8s.io/v1
kind: EncryptionConfiguration
resources:
- resources: [secrets]
  providers:
  - %s:
      keys:
      - name: key1
        secret: %s
  - identity: {}
`, mode, base64.StdEncoding.EncodeToString(key))
				require.NoError(t, os.WriteFile(path, []byte(config), 0600))
				args[name] = []string{"--encryption-provider-config=" + path, "--etcd-prefix=/shard/" + name}
			}
			server, restartDestination := privateMigrationServerWithArgs(t, args)
			client, err := kcpclientset.NewForConfig(server.BaseConfig(t))
			require.NoError(t, err)
			kube, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
			require.NoError(t, err)
			org, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithShard("root"))
			path, ws := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithShard("root"))
			secret, err := kube.Cluster(path).CoreV1().Secrets("default").Create(t.Context(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "migrated-secret"}, Data: map[string][]byte{"password": []byte("test-value")},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			bindExportForWildcardWatchTest(t, client, org, "migration.kcp.io", core.RootCluster.Path(), nil)
			_, err = client.Cluster(org).MigrationV1alpha1().LogicalClusterMigrations().Create(t.Context(), &migrationv1alpha1.LogicalClusterMigration{
				ObjectMeta: metav1.ObjectMeta{Name: "encrypted"}, Spec: migrationv1alpha1.LogicalClusterMigrationSpec{
					LogicalCluster: ws.Spec.Cluster, DestinationShard: "shard-1",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				migration, err := client.Cluster(org).MigrationV1alpha1().LogicalClusterMigrations().Get(t.Context(), "encrypted", metav1.GetOptions{})
				return err == nil && migration.Status.Phase == migrationv1alpha1.LogicalClusterMigrationPhaseCompleted
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
			checkSecret := func() {
				t.Helper()
				require.Eventually(t, func() bool {
					got, err := kube.Cluster(path).CoreV1().Secrets("default").Get(t.Context(), secret.Name, metav1.GetOptions{})
					return err == nil && got.UID == secret.UID && string(got.Data["password"]) == "test-value"
				}, wait.ForeverTestTimeout, 100*time.Millisecond, "migrated Secret must remain readable")
			}
			checkSecret()
			t.Log("Restarting destination to verify encrypted data survives cache rebuild")
			restartDestination()
			checkSecret()
		})
	}
}
