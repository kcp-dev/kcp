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

package base

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	"github.com/kcp-dev/logicalcluster/v3"
)

// workspaceOverrideClientConfig wraps a ClientConfig and overrides
// the server URL to point to a specific workspace.
type workspaceOverrideClientConfig struct {
	delegate  clientcmd.ClientConfig
	workspace string
}

func (c *workspaceOverrideClientConfig) RawConfig() (clientcmdapi.Config, error) {
	return c.delegate.RawConfig()
}

func (c *workspaceOverrideClientConfig) Namespace() (string, bool, error) {
	return c.delegate.Namespace()
}

func (c *workspaceOverrideClientConfig) ConfigAccess() clientcmd.ConfigAccess {
	return c.delegate.ConfigAccess()
}

func (c *workspaceOverrideClientConfig) ClientConfig() (*rest.Config, error) {
	delegateCfg, err := c.delegate.ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(delegateCfg)

	u, currentPath, err := pluginhelpers.ParseClusterURL(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("current URL %q does not point to a workspace: %w", cfg.Host, err)
	}

	name := c.workspace
	if strings.HasPrefix(name, ":") {
		name = strings.TrimPrefix(name, ":")
	} else {
		name = currentPath.Join(name).String()
	}

	pth, err := resolveWorkspaceDots(name)
	if err != nil {
		return nil, err
	}
	if !pth.IsValid() {
		return nil, fmt.Errorf("invalid workspace path: %s", c.workspace)
	}

	u.Path = path.Join(u.Path, pth.RequestPath())
	cfg.Host = u.String()
	return cfg, nil
}

// resolveWorkspaceDots resolves "." (stay) and ".." (go to parent) components in a colon-separated workspace path.
func resolveWorkspaceDots(pth string) (logicalcluster.Path, error) {
	var ret logicalcluster.Path
	for _, part := range strings.Split(pth, ":") {
		switch part {
		case ".":
			continue
		case "..":
			if ret.Empty() {
				return logicalcluster.Path{}, errors.New("cannot go above the root workspace")
			}
			ret, _ = ret.Parent()
		default:
			ret = ret.Join(part)
		}
	}
	return ret, nil
}
