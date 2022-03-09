/*
Copyright 2022 The KCP Authors.

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
	"strings"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/registry"
)

// prioritizedAuthInfo returns the first non-nil or non-empty AuthInfo it finds
// from the ordred list passed in argument
func prioritizedAuthInfo(values ...*api.AuthInfo) *api.AuthInfo {
	for _, value := range values {
		if value == nil {
			continue
		}
		value := *value
		if value.Token != "" || value.TokenFile != "" || value.Password != "" || value.Username != "" ||
			value.Exec != nil || value.AuthProvider != nil {
			return &value
		}
	}
	return api.NewAuthInfo()
}

// extractScopeAndName extracts the scope and workspace name from
// workspace kubeconfig context names as they are set in workspace Kubeconfigs
// retrieved from the virtual workspace kubeconfig sub-resource
func extractScopeAndName(workspaceKey string) (string, string) {
	if strings.HasPrefix(workspaceKey, registry.PersonalScope+"/") {
		return registry.PersonalScope, strings.TrimPrefix(workspaceKey, registry.PersonalScope+"/")
	} else if strings.HasPrefix(workspaceKey, registry.OrganizationScope+"/") {
		return registry.OrganizationScope, strings.TrimPrefix(workspaceKey, registry.OrganizationScope+"/")
	}
	return "", workspaceKey
}

func write(opts *Options, str string) error {
	_, err := opts.Out.Write([]byte(str))
	return err
}
