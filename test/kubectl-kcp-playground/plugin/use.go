/*
Copyright 2023 The KCP Authors.

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
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type UseMode string

const (
	UseShardMode    = "shard"
	UsePClusterMode = "p-cluster"
)

type UseOptions struct {
	genericclioptions.IOStreams

	Spec *PlaygroundSpec

	Mode UseMode
	Name string
}

func NewUseOptions(streams genericclioptions.IOStreams) *UseOptions {
	return &UseOptions{
		IOStreams: streams,
		Spec:      &PlaygroundSpec{},
	}
}

func (o *UseOptions) Complete(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("args must have one item")
	}
	o.Name = args[0]

	// We are using DetachedStartedPlaygroundFixture specifically to get access to the SpecPath
	// of the running playground only; this works because kcp plugin works with an hard code root directory.
	sp := newDetachedStartedPlaygroundFixture(o.Spec, WithPlaygroundRootDirectory(defaultRootDirectory))
	return o.Spec.CompleteFromFile(sp.SpecPath())
}

func (o *UseOptions) Validate() error {
	switch o.Mode {
	case UseShardMode:
		if !o.Spec.HasShard(o.Name) {
			return fmt.Errorf("shard '%s' does not exists", o.Name)
		}
	case UsePClusterMode:
		if !o.Spec.HasPCluster(o.Name) {
			return fmt.Errorf("pcluster '%s' does not exists", o.Name)
		}
	default:
		return fmt.Errorf("invalid mode '%s'", o.Mode)
	}

	return o.Spec.Validate(field.NewPath("<config>"))
}

func (o *UseOptions) BindFlags(_ *cobra.Command) {
	// TODO: add a flag for connecting to a shard as root, base or kcp-admin (currently only root supported)
}

func (o *UseOptions) Run(_ context.Context) error {
	if !startIsRunning() {
		fmt.Printf(" ⚠️️  Playground is not running, kubectl cannot connect to shards or pclusters\n")
		return nil
	}

	// We are using DetachedStartedPlaygroundFixture specifically to get access to the KubeConfigPath
	// of the running playground; this works because kcp plugin works with an hard code root directory.
	sp := newDetachedStartedPlaygroundFixture(o.Spec, WithPlaygroundRootDirectory(defaultRootDirectory))

	switch o.Mode {
	case UseShardMode:
		configPath := sp.KubeConfigPath()
		currentContext := contextForShard(o.Name)
		if err := setCurrentContext(configPath, currentContext); err != nil {
			return err
		}
		fmt.Printf(" 🔹️  Current context in kubeconfig '%s' is has been set to '%s' 🏓\n", configPath, currentContext)
	case UsePClusterMode:
		configPath := sp.KubeConfigPath()
		currentContext := contextForPCluster(o.Name)
		if err := setCurrentContext(configPath, currentContext); err != nil {
			return err
		}
		fmt.Printf(" 🔸  Current context in kubeconfig '%s' is has been set to '%s' 🏓\n", configPath, currentContext)
	default:
		return fmt.Errorf("invalid mode '%s'", o.Mode)
	}
	return nil
}
