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
	"net/http"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

const (
	defaultSpecPath = ".kcp-playground.yaml"
	MainShardName   = "main"
)

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

// PlaygroundSpec defines spec for a PlaygroundFixture.
type PlaygroundSpec struct {
	// Shards to be created as part of the playground.
	// NOTE: As of today playground supports one shard only, and it must be named "main".
	Shards []Shard `json:"shards,omitempty"`

	// PClusters to be created as part of the playground.
	PClusters []PCluster `json:"pClusters,omitempty"`
}

// Shard defines a Shard to be created as part of the playground.
type Shard struct {
	// Name of the Shard.
	Name string `json:"name,omitempty"`

	// Workspaces allows to define initial workspace configuration for the Shard.
	// NOTE: As of today playground supports one top level workspace, and it must be named "root".
	// NOTE: Workspaces are created in same order they are defined in the config file.
	Workspaces []Workspace `json:"workspaces,omitempty"`

	// DeploymentCoordinator defines if a deployment coordinator has to be run for this Shard.
	DeploymentCoordinator bool `json:"deploymentCoordinator,omitempty"`
}

// Workspace initial configuration.
type Workspace struct {
	// Name of the Workspace.
	Name string `json:"name,omitempty"`

	// Type of the Workspace.
	Type tenancyv1alpha1.WorkspaceTypeReference `json:"type,omitempty"`

	// SyncTarget to be added to the workspace.
	SyncTargets []SyncTarget `json:"syncTargets,omitempty"`

	// Location to be added to the workspace.
	Locations []Location `json:"locations,omitempty"`

	// Placement to be added to the workspace.
	Placements []Placement `json:"placements,omitempty"`

	// APIResourceSchema to be added to the workspace.
	APIResourceSchemas []APIResourceSchema `json:"apiResourceSchemas,omitempty"`

	// APIExport to be added to the workspace.
	APIExports []APIExport `json:"apiExports,omitempty"`

	// APIBinding to be added to the workspace.
	APIBindings []APIBinding `json:"apiBindings,omitempty"`

	// Others defines additional resources to be added to the workspace.
	Others []Other `json:"others,omitempty"`

	// Workspaces nested into this workspace.
	Workspaces []Workspace `json:"workspaces,omitempty"`
}

// SyncTarget to be added to the workspace.
// NOTE: Defining SyncTarget is equivalent to run `kubectl kcp workload sync`; the generated
// syncer yaml will be applied to the target pCluster.
type SyncTarget struct {
	// Name of the SyncTarget.
	Name string `json:"name,omitempty"`

	// Labels to be applied to the SyncTarget.
	Labels map[string]string `json:"labels,omitempty" protobuf:"bytes,11,rep,name=labels"`

	// PCluster the SyncTarget targets to; this is where the syncer yaml will be applied.
	PCluster string `json:"pCluster,omitempty"`

	// APIExports to be supported by this SyncTarget
	APIExports []apisv1alpha1.ExportBindingReference `json:"apiExports,omitempty"`

	// Resources to synchronize with kcp, each resource should be in the format of resourcename.<gvr_of_the_resource>.
	Resources []string `json:"resources,omitempty"`
}

// Location to be added to the workspace.
type Location struct {
	// Name of the Location.
	Name string `json:"name,omitempty"`

	// Labels to be applied to the Location.
	Labels map[string]string `json:"labels,omitempty" protobuf:"bytes,11,rep,name=labels"`

	// InstanceSelector to be used to select instances/synctargets
	// to be included in this location.
	InstanceSelector *metav1.LabelSelector `json:"instanceSelector,omitempty"`
}

// Placement to be added to the workspace.
// NOTE: Defining SyncTarget is equivalent to run `kubectl kcp bind compute`.
type Placement struct {
	// Name of the Placement.
	Name string `json:"name,omitempty"`

	// LocationWorkspace defines the workspace where Location are defined.
	LocationWorkspace string `json:"locationWorkspace,omitempty"`

	// LocationSelectors allows to select Location.
	LocationSelectors []metav1.LabelSelector `json:"locationSelectors,omitempty"`
}

// APIResourceSchema to be added to the workspace; it is generated from a
// source that can be either a CustomResource or APIResourceSchema.
// NOTE: The conversion from CRD to APIResourceSchema is equivalent to run `kubectl crd snapshot`.
type APIResourceSchema struct {
	// Name of the APIResourceSchema.
	Name string `json:"name,omitempty"`

	// Source of the APIResourceSchema.
	// NOTE: In order to allow easy import of existing CRDs, the source can contain more than one
	// CRD/APIResourceSchema; as a consequence, the name/number of generated APIResourceSchema might
	// differ from the one defined in the config file.
	Source APIResourceSchemaSource `json:"source,omitempty"`
}

// APIResourceSchemaSource is a source of the APIResourceSchema.
type APIResourceSchemaSource struct {
	Source

	// Prefix to be applied to Custom resource definition when converted to APIResourceSchema.
	Prefix string `json:"prefix,omitempty"`
}

// APIExport to be added to the workspace.
type APIExport struct {
	// Name of the APIExport.
	Name string `json:"name,omitempty"`

	// APIResourceSchemas to be exported.
	APIResourceSchemas []string `json:"apiResourceSchemas,omitempty"`
}

// APIBinding to be added to the workspace.
type APIBinding struct {
	// Name of the APIBinding.
	Name string `json:"name,omitempty"`

	// APIExport to bind to.
	APIExport apisv1alpha1.ExportBindingReference `json:"apiExport,omitempty"`
}

// PCluster defines a compute cluster to be created as part of the playground.
type PCluster struct {
	// Name of the PCluster.
	// You should use this name to refer to this PCluster from SyncTargets.
	Name string `json:"name,omitempty"`

	// Type of the PCluster.
	// NOTE: currently only PCluster of type 'kind' are supported.
	Type PClusterType `json:"type,omitempty"`

	// TODO: allow setting syncer image; TBD if this should be defined for each pCluster or once for each playground
	// TODO: add support for the deployment coordination; TBD if this should be defined for each pCluster or once for each playground

	// Others defines additional resources to be added to the PCluster.
	Others []Other `json:"others,omitempty"`
}

// PClusterType defines type of the PCluster.
type PClusterType string

// KindPClusterType identifies PCluster created with kind.
const KindPClusterType = "kind"

// Other resource to be added to the PCluster.
type Other struct {
	// Name of the other resource.
	Name string `json:"name,omitempty"`

	// Source of the other resource yaml.
	Source Source `json:"source,omitempty"`
}

// Source of the other resource yaml.
// Only one of Raw and File can be used.
type Source struct {
	// Raw resource yaml.
	Raw *string `json:"raw,omitempty"`

	// File containing the resource yaml.
	File *FileSource `json:"file,omitempty"`
}

// FileSource for resource yaml.
type FileSource struct {
	// Path to the file.
	Path string `json:"path,omitempty"`

	// Url to the file.
	Url string `json:"url,omitempty"`
}

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
		s.Shards = []Shard{
			{
				Name: MainShardName,
			},
		}
	}

	// TODO: add more completion rules
	//   - empty paths are set to current workspace

	return nil
}

func (s *PlaygroundSpec) Validate(path *field.Path) error {
	if len(s.Shards) != 1 || s.Shards[0].Name != MainShardName {
		return field.Invalid(path.Child("shards"), s.Shards, "playground currently supports only one shard named 'main', stay tuned!")
	}

	if len(s.Shards[0].Workspaces) != 1 || s.Shards[0].Workspaces[0].Name != "root" {
		return field.Invalid(path.Child("shards"), s.Shards, "playground currently supports only root as a top level workspace, stay tuned!")
	}

	// TODO: add more validation rules
	// - check if kind and ko are installed in the cluster (only if used)
	// - check if Kind clusters do not already exists
	// - validate object names against api names regex
	// - validate pcluster kind
	// - validate kind cluster names
	// - cross check name references are consistent (e.g. SyncTargets --> pCluster)
	// - only one of raw and file can be set in Source

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

func (r *Source) Get() ([]byte, error) {
	if r.Raw != nil {
		return []byte(*r.Raw), nil
	}
	if r.File != nil {
		return r.File.Get()
	}
	return nil, nil
}

func (fs *FileSource) Get() ([]byte, error) {
	if fs.Path != "" {
		return os.ReadFile(fs.Path)
	}
	if fs.Url != "" {
		resp, err := http.Get(fs.Url) //nolint:noctx
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = resp.Body.Close()
		}()
		return io.ReadAll(resp.Body)
	}
	return nil, nil
}
