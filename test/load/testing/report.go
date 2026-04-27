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
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
)

// NewKCPReport creates a new Report and adds metadata about kcp its running against.
func NewKCPReport(t *testing.T, title string, cfg *rest.Config) *measurement.Report {
	t.Helper()

	report := measurement.NewReport(title)

	c, err := kcpclientset.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("Could not build kcp client: %v", err)
	}
	rootclient := c.Cluster(core.RootCluster.Path())

	sv, err := rootclient.Discovery().ServerVersion()
	if err != nil {
		t.Fatalf("Could not fetch kcp version: %v", err)
	}

	report.Metadata = append(report.Metadata, measurement.Parameter{Key: "kcp Version", Value: sv.GitVersion})

	shards, err := rootclient.CoreV1alpha1().Shards().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Could not list kcp shards: %v", err)
	}

	report.Metadata = append(report.Metadata, measurement.Parameter{Key: "kcp Shards", Value: fmt.Sprintf("%d", len(shards.Items))})

	return report
}
