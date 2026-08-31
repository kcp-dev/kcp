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

// Package plugin implements shard operations, e.g. cordoning and uncordoning
// a shard the way kubectl cordon/uncordon act on nodes.
package plugin

import (
	"context"
	"fmt"
	"net/url"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/cli/pkg/base"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

// CordonOptions holds the options for cordoning or uncordoning a shard.
type CordonOptions struct {
	*base.Options

	// Shard is the name of the shard to act on.
	Shard string
	// Cordon marks the shard unschedulable when true, schedulable again when
	// false.
	Cordon bool

	// for testing
	newKCPClusterClient func(clientConfig clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error)
}

// NewCordonOptions returns a new CordonOptions.
func NewCordonOptions(streams genericclioptions.IOStreams) *CordonOptions {
	options := base.NewOptions(streams)
	// Shard objects live in the root workspace only; a --workspace flag would
	// be misleading.
	options.OptOutOfWorkspaceFlag = true
	return &CordonOptions{
		Options:             options,
		newKCPClusterClient: newKCPClusterClient,
	}
}

// Complete ensures all dynamically populated fields are initialized.
func (o *CordonOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}
	if len(args) > 0 {
		o.Shard = args[0]
	}
	return nil
}

// Validate validates the inputs.
func (o *CordonOptions) Validate() error {
	if o.Shard == "" {
		return fmt.Errorf("shard name is required")
	}
	return o.Options.Validate()
}

// Run (un)cordons the shard by updating the unschedulable annotation on the
// Shard object in the root workspace. Like a node's spec.unschedulable in
// Kubernetes, the annotation is the desired state; the owning shard
// acknowledges it via the Schedulable condition on the Shard object.
func (o *CordonOptions) Run(ctx context.Context) error {
	kcpClusterClient, err := o.newKCPClusterClient(o.ClientConfig)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}
	shards := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards()

	shard, err := shards.Get(ctx, o.Shard, metav1.GetOptions{})
	if err != nil {
		return err
	}

	// the scheduler checks for the presence of the annotation, not its value.
	_, cordoned := shard.Annotations[corev1alpha1.ShardUnschedulableAnnotationKey]

	var patch string
	switch {
	case o.Cordon && cordoned:
		fmt.Fprintf(o.Out, "shard/%s already cordoned\n", o.Shard)
		return nil
	case !o.Cordon && !cordoned:
		fmt.Fprintf(o.Out, "shard/%s already uncordoned\n", o.Shard)
		return nil
	case o.Cordon:
		patch = fmt.Sprintf(`{"metadata":{"annotations":{%q:"true"}}}`, corev1alpha1.ShardUnschedulableAnnotationKey)
	default:
		patch = fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, corev1alpha1.ShardUnschedulableAnnotationKey)
	}

	if _, err := shards.Patch(ctx, o.Shard, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return err
	}

	if o.Cordon {
		fmt.Fprintf(o.Out, "shard/%s cordoned\n", o.Shard)
	} else {
		fmt.Fprintf(o.Out, "shard/%s uncordoned\n", o.Shard)
	}
	return nil
}

func newKCPClusterClient(clientConfig clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	clusterConfig := rest.CopyConfig(config)
	u, err := url.Parse(config.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	return kcpclientset.NewForConfig(clusterConfig)
}
