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

package plugin

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcpfakeclient "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
)

func shardObj(name string, cordoned bool) *corev1alpha1.Shard {
	shard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: core.RootCluster.String()},
		},
	}
	if cordoned {
		shard.Annotations[corev1alpha1.ShardUnschedulableAnnotationKey] = "true"
	}
	return shard
}

func run(t *testing.T, shard *corev1alpha1.Shard, cordon bool) (*kcpfakeclient.ClusterClientset, string) {
	t.Helper()
	client := kcpfakeclient.NewSimpleClientset(shard) //nolint:staticcheck
	out := &bytes.Buffer{}
	opts := NewCordonOptions(genericclioptions.IOStreams{Out: out, ErrOut: out})
	opts.Shard = shard.Name
	opts.Cordon = cordon
	opts.newKCPClusterClient = func(clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
		return client, nil
	}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := opts.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return client, out.String()
}

func getShard(t *testing.T, client *kcpfakeclient.ClusterClientset, name string) *corev1alpha1.Shard {
	t.Helper()
	shard, err := client.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return shard
}

func TestCordon(t *testing.T) {
	t.Parallel()
	client, out := run(t, shardObj("alpha", false), true)
	if !strings.Contains(out, "shard/alpha cordoned") {
		t.Errorf("unexpected output: %q", out)
	}
	if _, ok := getShard(t, client, "alpha").Annotations[corev1alpha1.ShardUnschedulableAnnotationKey]; !ok {
		t.Error("expected the unschedulable annotation to be set")
	}
}

func TestCordonAlreadyCordoned(t *testing.T) {
	t.Parallel()
	_, out := run(t, shardObj("alpha", true), true)
	if !strings.Contains(out, "shard/alpha already cordoned") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestUncordon(t *testing.T) {
	t.Parallel()
	client, out := run(t, shardObj("alpha", true), false)
	if !strings.Contains(out, "shard/alpha uncordoned") {
		t.Errorf("unexpected output: %q", out)
	}
	if _, ok := getShard(t, client, "alpha").Annotations[corev1alpha1.ShardUnschedulableAnnotationKey]; ok {
		t.Error("expected the unschedulable annotation to be removed")
	}
}

func TestUncordonAlreadyUncordoned(t *testing.T) {
	t.Parallel()
	_, out := run(t, shardObj("alpha", false), false)
	if !strings.Contains(out, "shard/alpha already uncordoned") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestValidateRequiresShardName(t *testing.T) {
	t.Parallel()
	opts := NewCordonOptions(genericclioptions.IOStreams{})
	if err := opts.Validate(); err == nil {
		t.Error("expected an error when no shard name is given")
	}
}
