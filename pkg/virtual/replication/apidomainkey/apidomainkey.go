/*
Copyright 2025 The kcp Authors.

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

package apidomainkey

import (
	"context"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

func New(cluster logicalcluster.Name, name string) dynamiccontext.APIDomainKey {
	return dynamiccontext.APIDomainKey(fmt.Sprintf("%s/%s", cluster, name))
}

type Key struct {
	Cluster logicalcluster.Name
	Name    string
}

func FromContext(ctx context.Context) (*Key, error) {
	return Parse(dynamiccontext.APIDomainKeyFrom(ctx))
}

func Parse(key dynamiccontext.APIDomainKey) (*Key, error) {
	parts := strings.Split(string(key), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid APIDomainKey %q for replication VW", string(key))
	}

	return &Key{
		Cluster: logicalcluster.Name(parts[0]),
		Name:    parts[1],
	}, nil
}
