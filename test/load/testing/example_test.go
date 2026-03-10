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

package workspace

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

func TestExample(t *testing.T) {
	// request any capabilities from the user
	cfg := framework.Require(t,
		framework.KCPFrontProxyKubeconfig,
		framework.KCPShardKubeconfig,
	)
	t.Logf("front-proxy kubeconfig host: %s", cfg.FrontProxyKubeconfig.Host)
	t.Logf("shard kubeconfig host: %s", cfg.ShardKubeconfig.Host)

	// setup a tuning set
	ts := tuningset.NewUniformQPS(15, 30, 0)

	// configure any stats
	sink := &measurement.Memory{
		Stats: []stats.NamedStat{
			stats.P99(),
			stats.Avg(),
		},
	}

	action := func(seq int, sink measurement.Sink) error {
		// register the duration of the action as a measurement
		defer measurement.RecordElapsedDurationMS(time.Now(), sink)

		time.Sleep(time.Duration(seq) * 10 * time.Millisecond)

		return nil
	}

	// run the actual action
	errs := framework.Execute(ts, action, sink)

	require.Empty(t, errs)

	// print results
	fmt.Println(sink.Results())
}
