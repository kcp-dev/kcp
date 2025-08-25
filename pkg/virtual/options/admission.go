/*
Copyright 2025 The KCP Authors.

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

package options

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/admission"
	admissionmetrics "k8s.io/apiserver/pkg/admission/metrics"
	apiserverapi "k8s.io/apiserver/pkg/apis/apiserver"
	apiserverapiv1 "k8s.io/apiserver/pkg/apis/apiserver/v1"
	apiserverapiv1alpha1 "k8s.io/apiserver/pkg/apis/apiserver/v1alpha1"
	genericapiserver "k8s.io/apiserver/pkg/server"

	vwinitializers "github.com/kcp-dev/kcp/pkg/virtual/framework/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/admission/plugins"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

var configScheme = runtime.NewScheme()

func init() {
	utilruntime.Must(apiserverapi.AddToScheme(configScheme))
	utilruntime.Must(apiserverapiv1alpha1.AddToScheme(configScheme))
	utilruntime.Must(apiserverapiv1.AddToScheme(configScheme))
}

type Admission struct {
}

func NewAdmission() *Admission {
	return &Admission{}
}

func (s *Admission) ApplyTo(config *genericapiserver.Config, virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace) error {
	if s == nil {
		return nil
	}

	if virtualWorkspaces == nil {
		return fmt.Errorf("admission depends on virtual workspaces, it cannot be nil")
	}

	pluginNames := plugins.AllOrderedPlugins

	pluginsConfigProvider, err := admission.ReadAdmissionConfiguration(pluginNames, "", configScheme)
	if err != nil {
		return fmt.Errorf("failed to read plugin config: %v", err)
	}

	p := admission.NewPlugins()
	plugins.RegisterAllVWAdmissionPlugins(p)

	initializersChain := admission.PluginInitializers{vwinitializers.NewVirtualWorkspacesInitializer(virtualWorkspaces)}

	admissionChain, err := p.NewFromPlugins(pluginNames, pluginsConfigProvider, initializersChain, nil)
	if err != nil {
		return err
	}

	config.AdmissionControl = admissionmetrics.WithStepMetrics(admissionChain)

	return nil
}
