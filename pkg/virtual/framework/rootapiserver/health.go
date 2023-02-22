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

package rootapiserver

import (
	"net/http"

	"k8s.io/apiserver/pkg/server/healthz"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
)

type asHealthCheck struct {
	name string
	framework.VirtualWorkspace
}

func (vw asHealthCheck) Name() string {
	return vw.name
}

func (vw asHealthCheck) Check(req *http.Request) error {
	return vw.IsReady()
}

func asHealthChecks(workspaces []NamedVirtualWorkspace) []healthz.HealthChecker {
	healthCheckers := make([]healthz.HealthChecker, 0, len(workspaces))
	for _, vw := range workspaces {
		healthCheckers = append(healthCheckers, asHealthCheck{name: vw.Name, VirtualWorkspace: vw})
	}
	return healthCheckers
}
