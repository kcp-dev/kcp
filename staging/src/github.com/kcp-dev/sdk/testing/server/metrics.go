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

package server

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	"github.com/kcp-dev/sdk/testing/server/prometheus"
)

func gatherMetrics(ctx context.Context, t TestingT, server RunningServer, directory string) {
	cfg := server.RootShardSystemMasterBaseConfig(t)
	client, err := kcpclientset.NewForConfig(cfg)
	if err != nil {
		// Don't fail the test if we couldn't scrape metrics
		t.Logf("error creating metrics client for server %s: %v", server.Name(), err)
	}

	raw, err := client.RESTClient().Get().RequestURI("/metrics").DoRaw(ctx)
	if err != nil {
		// Don't fail the test if we couldn't scrape metrics
		t.Logf("error getting metrics for server %s: %v", server.Name(), err)
		return
	}

	metricsFile := filepath.Join(directory, fmt.Sprintf("%s-metrics.txt", server.Name()))
	if err := os.WriteFile(metricsFile, raw, 0o644); err != nil {
		// Don't fail the test if we couldn't scrape metrics
		t.Logf("error writing metrics file %s: %v", metricsFile, err)
	}
}

func scrapeMetricsForServer(t TestingT, srv RunningServer) {
	promUrl, set := os.LookupEnv("PROMETHEUS_URL")
	if !set || promUrl == "" {
		t.Logf("PROMETHEUS_URL environment variable unset, skipping Prometheus scrape config generation")
		return
	}

	caFile := filepath.Join(srv.CADirectory(), "apiserver.crt")
	if _, err := os.Stat(caFile); os.IsNotExist(err) {
		t.Logf("CA file %s does not exist, skipping Prometheus scrape config for server %s", caFile, srv.Name())
		return
	}

	jobName := fmt.Sprintf("kcp-%s-%s-%d", srv.Name(), t.Name(), time.Now().Unix())
	labels := map[string]string{
		"server": srv.Name(),
		"test":   t.Name(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	defer cancel()
	repoDir, err := kcptestinghelpers.RepositoryDir()
	if err != nil {
		t.Logf("error getting repository directory for server %s: %v", srv.Name(), err)
		return
	}

	if err := ScrapeMetrics(ctx, srv.RootShardSystemMasterBaseConfig(t), promUrl, repoDir, jobName, caFile, labels); err != nil {
		t.Logf("error configuring Prometheus scraping for server %s: %v", srv.Name(), err)
	}

	// Clean up Prometheus configuration when test finishes
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := CleanupScrapeMetrics(cleanupCtx, promUrl, repoDir, jobName); err != nil {
			t.Logf("error cleaning up Prometheus scrape config for server %s: %v", srv.Name(), err)
		}
	})
}

func prometheusConfigFile(cfgDir string) string {
	return filepath.Join(cfgDir, ".prometheus-config.yaml")
}

func ScrapeMetrics(ctx context.Context, cfg *rest.Config, promUrl, promCfgDir, jobName, caFile string, labels map[string]string) error {
	hostUrl, err := url.Parse(cfg.Host)
	if err != nil {
		return err
	}

	if err := prometheus.EnsureScrapeConfig(prometheusConfigFile(promCfgDir), prometheus.ScrapeConfig{
		JobName:        jobName,
		ScrapeInterval: (5 * time.Second).String(),
		BearerToken:    cfg.BearerToken,
		TlsConfig:      prometheus.TLSConfig{CaFile: caFile},
		Scheme:         hostUrl.Scheme,
		StaticConfigs: []prometheus.StaticConfig{{
			Targets: []string{hostUrl.Host},
			Labels:  labels,
		}},
	}); err != nil {
		return fmt.Errorf("failed to update Prometheus configuration file: %w", err)
	}

	if err := prometheus.Reload(ctx, promUrl); err != nil {
		return fmt.Errorf("error reloading Prometheus: %w", err)
	}

	return nil
}

func CleanupScrapeMetrics(ctx context.Context, promUrl, promCfgDir, jobName string) error {
	if err := prometheus.RemoveScrapeConfig(prometheusConfigFile(promCfgDir), jobName); err != nil {
		return fmt.Errorf("failed to update Prometheus configuration file: %w", err)
	}

	if err := prometheus.Reload(ctx, promUrl); err != nil {
		return fmt.Errorf("error reloading Prometheus: %w", err)
	}

	return nil
}
