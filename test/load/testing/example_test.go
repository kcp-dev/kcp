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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

func TestExample(t *testing.T) {
	// note: this will be set by the test runner / user externally, it's just here for demonstration purposes
	fakeKubeconfig(t)

	// request any capabilities from the user
	cfg := framework.Require(t,
		framework.KCPFrontProxyKubeconfig,
	)
	t.Logf("front-proxy kubeconfig host: %s", cfg.FrontProxyKubeconfig.Host)

	// each section contains metatada and a datasink
	section := measurement.Section{
		Title: "Example Action 1",
		Parameters: []measurement.Parameter{
			{Key: "Count", Value: fmt.Sprintf("%d", 30)},
			{Key: "QPS", Value: fmt.Sprintf("%f", 15.0)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	// tuningsets control the execution of a section
	ts := tuningset.NewUniformQPS(15, 30, 0)
	// this helper can be used as a control variable for section timing
	section.Start()
	section.Errors = framework.Execute(ts, func(seq int, s measurement.Sink) error {
		// automatically measure how long each action took
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		time.Sleep(time.Duration(seq) * 10 * time.Millisecond)

		return nil
	}, section.Sink)
	section.End()

	// print out section results in a human-friendly format
	report := measurement.NewReport("Example")
	report.Sections = []measurement.Section{section}
	report.PrettyPrint(os.Stdout)

	// Fail the test if any section had errors.
	require.Empty(t, section.Errors, "section %q encountered errors", section.Title)
}

// fakeKubeconfig creates a synthetic kubeconfig in a temp directory and
// sets the corresponding env var so framework.Require can pick it up.
func fakeKubeconfig(t *testing.T) {
	t.Helper()

	kubeconfig := clientcmdapi.NewConfig()
	kubeconfig.Clusters["kcp"] = &clientcmdapi.Cluster{Server: "https://example.com"}
	kubeconfig.AuthInfos["admin"] = &clientcmdapi.AuthInfo{Token: "admin-token"}
	kubeconfig.Contexts["default"] = &clientcmdapi.Context{Cluster: "kcp", AuthInfo: "admin"}
	kubeconfig.CurrentContext = "default"

	data, err := clientcmd.Write(*kubeconfig)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, data, 0600))

	t.Setenv(string(framework.KCPFrontProxyKubeconfig), path)
}
