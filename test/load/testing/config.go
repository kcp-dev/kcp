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

package testing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/test/load/pkg/tree"
)

var testConfig Config

// Config is the top-level configuration structure unmarshalled from the
// JSON config file passed via --config.
type Config struct {
	// add as field here so our example passes stric decoding
	Comment string `json:"_comment,omitempty"`

	Params Params `json:"params"`
}

func defaultConfig() Config {
	return Config{
		Params: defaultParams(),
	}
}

func defaultParams() Params {
	return Params{
		WorkspaceCount:          10000,
		WorkspaceDepth:          5,
		WorkspaceTreeType:       "symmetric",
		CreateWorkspaceQPS:      8.0,
		CRUDConfigMapQPS:        150,
		ProviderWorkspacesCount: 1000,
		ConsumerWorkspacesCount: 9000,
		BindingsPerConsumer:     1,
		CreateAPIExportQPS:      4.0,
		CreateAPIBindingQPS:     4.0,
		CRUDSharedAPIQPS:        150,
		CRLeafFields:            160,
		CRListItems:             30,
		CRTargetSizeBytes:       6000,
	}
}

// Params holds all configurable load test parameters. Values can be
// overridden by passing --config=<path-to-json> to the test binary.
// Any field not set in the JSON file retains its default value.
type Params struct {
	// WorkspaceCount is the total number of workspaces to create.
	WorkspaceCount int `json:"workspaceCount"`

	// WorkspaceDepth controls how deep the workspace tree is.
	WorkspaceDepth int `json:"workspaceDepth"`

	// WorkspaceTreeType selects the tree layout: "symmetric" or "flat".
	WorkspaceTreeType string `json:"workspaceTreeType"`

	// CreateWorkspaceQPS is the rate at which workspace creation requests are sent.
	CreateWorkspaceQPS float64 `json:"createWorkspaceQPS"`

	// CRUDConfigMapQPS is the rate at which ConfigMap CRUD operations are sent.
	CRUDConfigMapQPS float64 `json:"crudConfigMapQPS"`

	// ProviderWorkspacesCount is the number of provider workspaces for API sharing tests.
	ProviderWorkspacesCount int `json:"providerWorkspacesCount"`

	// ConsumerWorkspacesCount is the number of consumer workspaces for API sharing tests.
	ConsumerWorkspacesCount int `json:"consumerWorkspacesCount"`

	// BindingsPerConsumer is the number of APIBindings each consumer workspace creates.
	// Providers are assigned via round-robin across the total binding slots
	// (consumerWorkspacesCount * bindingsPerConsumer)
	BindingsPerConsumer int `json:"bindingsPerConsumer"`

	// CreateAPIExportQPS is the rate at which APIExport creation requests are sent.
	CreateAPIExportQPS float64 `json:"createAPIExportQPS"`

	// CreateAPIBindingQPS is the rate at which APIBinding creation requests are sent.
	CreateAPIBindingQPS float64 `json:"createAPIBindingQPS"`

	// CRUDSharedAPIQPS is the rate at which custom resource CRUD operations are sent.
	CRUDSharedAPIQPS float64 `json:"crudSharedAPIQPS"`

	// CRLeafFields is the number of scalar string properties under spec.
	// The first field is always "data" and is used for the CRUD update path.
	CRLeafFields int `json:"crLeafFields"`

	// CRListItems is the number of items in a spec.items[] array.
	// Each item has 3 string sub-fields (name, value, description).
	// 0 means no list field is generated.
	CRListItems int `json:"crListItems"`

	// CRTargetSizeBytes pads spec.data to reach this approximate total CR JSON size.
	// 0 means no padding is applied.
	CRTargetSizeBytes int `json:"crTargetSizeBytes"`
}

// WorkspaceTree returns a workspace tree built from the configured parameters.
func (p Params) WorkspaceTree() tree.WorkspaceTree {
	switch p.WorkspaceTreeType {
	case "symmetric":
		return tree.NewSymmetricTree(core.RootCluster.Path(), p.WorkspaceCount, p.WorkspaceDepth)
	case "flat":
		return tree.NewFlatTree(core.RootCluster.Path(), p.WorkspaceCount)
	default:
		return nil
	}
}

func parseConfig(configFile string) error {
	testConfig = defaultConfig()

	if configFile == "" {
		return nil
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file %q: %w", configFile, err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&testConfig); err != nil {
		return fmt.Errorf("failed to parse config file %q: %w", configFile, err)
	}

	if testConfig.Params.BindingsPerConsumer > testConfig.Params.ProviderWorkspacesCount {
		return fmt.Errorf("bindingsPerConsumer (%d) must not exceed providerWorkspacesCount (%d) — a consumer cannot bind to the same provider twice",
			testConfig.Params.BindingsPerConsumer, testConfig.Params.ProviderWorkspacesCount)
	}

	if testConfig.Params.CRLeafFields < 1 {
		return fmt.Errorf("crLeafFields must be >= 1, got %d", testConfig.Params.CRLeafFields)
	}
	if testConfig.Params.CRListItems < 0 {
		return fmt.Errorf("crListItems must be >= 0, got %d", testConfig.Params.CRListItems)
	}
	if testConfig.Params.CRTargetSizeBytes < 0 {
		return fmt.Errorf("crTargetSizeBytes must be >= 0, got %d", testConfig.Params.CRTargetSizeBytes)
	}

	return nil
}
