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
	"fmt"
	"io"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

const (
	defaultSpecPath = ".kcp-playground.yaml"
	MainShardName   = "main"
)

// TODO: Add more comments

// Playground allows to define how the environment for a PlaygroundFixture should look like, so
// the users/developers can focus on testing/experimenting on top of it (instead of provisioning it).
//
// NOTE: This API is exposing a small subset of features from KCP API objects;
// This set is expected to grow while we implement more capabilities to the playground engine;
// when the functional coverage grows we should consider if to use Spec KCP API objects, but
// for now keeping this as light as possible.
type Playground struct {
	metav1.TypeMeta `json:",inline"`
	Spec            PlaygroundSpec `json:"spec,omitempty"`
}

type PlaygroundSpec struct {
	Shards []ShardSpec `json:"shards,omitempty"`

	PClusters []PClusterSpec `json:"pClusters,omitempty"`
}

type ShardSpec struct {
	Name string `json:"name,omitempty"`

	Workspaces []Workspace `json:"workspaces,omitempty"`
}

type Workspace struct {
	Name string `json:"name,omitempty"`

	Type tenancyv1alpha1.WorkspaceTypeReference `json:"type,omitempty"`

	Workspaces []Workspace `json:"workspaces,omitempty"`

	SyncTargets []SyncTarget `json:"syncTargets,omitempty"`

	// TODO: support defining locations

	Placements []Placement `json:"placements,omitempty"`
}

type SyncTarget struct {
	Name string `json:"name,omitempty"`

	PCluster string `json:"pCluster,omitempty"`

	// TODO: support labeling sync targets
}

type Placement struct {
	Name string `json:"name,omitempty"`

	LocationWorkspace string `json:"locationWorkspace,omitempty"`

	// TODO: support LocationSelectors
}

type PClusterSpec struct {
	Name string `json:"name,omitempty"`

	Type PClusterType `json:"type,omitempty"`

	// TODO: allow setting syncer image; TBD if this should be defined for each pCluster or once for each playground
	// TODO: add support for the deployment coordination; TBD if this should be defined for each pCluster or once for each playground
}

type PClusterType string

const KindPClusterType = "kind"

func (s *PlaygroundSpec) CompleteFromFile(specPath string) error {
	if specPath == "" {
		return fmt.Errorf("specPath cannot be empty")
	}
	if err := s.completeFromFile(specPath); err != nil {
		return err
	}
	return s.complete()
}

func (s *PlaygroundSpec) complete() error {
	if len(s.Shards) == 0 {
		s.Shards = []ShardSpec{
			{
				Name: MainShardName,
			},
		}
	}
	return nil
}

func (s *PlaygroundSpec) Validate(path *field.Path) error {
	if len(s.Shards) == 0 {
		return field.Required(path.Child("shards"), "playground currently supports only one shard, stay tuned!")
	}

	if len(s.Shards) > 1 || s.Shards[0].Name != MainShardName {
		return field.Invalid(path.Child("shards"), s.Shards, "playground currently supports only one shard named 'main', stay tuned!")
	}

	// TODO: add more validation rules
	// - check if kind and ko are installed in the cluster (only if used)
	// - check if Kind clusters do not already exists
	// - validate object names against api names regex
	// - validate pcluster kind
	// - validate kind cluster names
	// - cross check name references are consistent (e.g. SyncTargets --> pCluster)

	return nil
}

func (s *PlaygroundSpec) ShardNames() []string {
	names := []string{}
	for _, shard := range s.Shards {
		names = append(names, shard.Name)
	}
	return names
}

func (s *PlaygroundSpec) PClusterNames() []string {
	names := []string{}
	for _, pcluster := range s.PClusters {
		names = append(names, pcluster.Name)
	}
	return names
}

func (s *PlaygroundSpec) HasShard(name string) bool {
	for _, shard := range s.Shards {
		if shard.Name == name {
			return true
		}
	}
	return false
}

func (s *PlaygroundSpec) HasPCluster(name string) bool {
	for _, shard := range s.PClusters {
		if shard.Name == name {
			return true
		}
	}
	return false
}

func (s *PlaygroundSpec) WriteToFile(path string) error {
	data, err := yaml.Marshal(Playground{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "test.kcp.io/v1alpha1",
			Kind:       "Playground",
		},
		Spec: *s,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal playground config: %v", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write playground config to '%s': %v", path, err)
	}
	return nil
}

func (s *PlaygroundSpec) completeFromFile(path string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("failed open '%s' playground config file: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("failed read '%s' playground config file: %w", path, err)
	}

	// TODO: consider if to use api machinery (it requires to generate deepcopy), check API version, kind
	playground := &Playground{}
	if err := yaml.Unmarshal(data, playground); err != nil {
		return fmt.Errorf("failed unmarshal '%s' playground config file: %w", path, err)
	}

	*s = playground.Spec
	return nil
}
