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

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	gopkgyaml "gopkg.in/yaml.v3"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
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

	jobName := fmt.Sprintf("kcp-%s-%s", srv.Name(), t.Name())
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

func ScrapeMetrics(ctx context.Context, cfg *rest.Config, promUrl, promCfgDir, jobName, caFile string, labels map[string]string) error {
	jobName = fmt.Sprintf("%s-%d", jobName, time.Now().Unix())
	type staticConfigs struct {
		Targets []string          `yaml:"targets,omitempty"`
		Labels  map[string]string `yaml:"labels,omitempty"`
	}
	type tlsConfig struct {
		InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty"`
		CaFile             string `yaml:"ca_file,omitempty"`
	}
	type scrapeConfig struct {
		JobName        string          `yaml:"job_name,omitempty"`
		ScrapeInterval string          `yaml:"scrape_interval,omitempty"`
		BearerToken    string          `yaml:"bearer_token,omitempty"`
		TlsConfig      tlsConfig       `yaml:"tls_config,omitempty"`
		Scheme         string          `yaml:"scheme,omitempty"`
		StaticConfigs  []staticConfigs `yaml:"static_configs,omitempty"`
	}
	type config struct {
		ScrapeConfigs []scrapeConfig `yaml:"scrape_configs,omitempty"`
	}
	err := func() error {
		scrapeConfigFile := filepath.Join(promCfgDir, ".prometheus-config.yaml")
		f, err := os.OpenFile(scrapeConfigFile, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		// lock config file exclusively, blocks all other producers until unlocked or process (test) exits
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != nil {
			return err
		}
		promCfg := config{}
		err = gopkgyaml.NewDecoder(f).Decode(&promCfg)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		hostUrl, err := url.Parse(cfg.Host)
		if err != nil {
			return err
		}
		promCfg.ScrapeConfigs = append(promCfg.ScrapeConfigs, scrapeConfig{
			JobName:        jobName,
			ScrapeInterval: (5 * time.Second).String(),
			BearerToken:    cfg.BearerToken,
			TlsConfig:      tlsConfig{CaFile: caFile},
			Scheme:         hostUrl.Scheme,
			StaticConfigs: []staticConfigs{{
				Targets: []string{hostUrl.Host},
				Labels:  labels,
			}},
		})
		err = f.Truncate(0)
		if err != nil {
			return err
		}
		_, err = f.Seek(0, 0)
		if err != nil {
			return err
		}
		err = gopkgyaml.NewEncoder(f).Encode(&promCfg)
		if err != nil {
			return err
		}
		return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, promUrl+"/-/reload", http.NoBody)
	if err != nil {
		return err
	}
	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func CleanupScrapeMetrics(ctx context.Context, promUrl, promCfgDir, jobNamePrefix string) error {
	type staticConfigs struct {
		Targets []string          `yaml:"targets,omitempty"`
		Labels  map[string]string `yaml:"labels,omitempty"`
	}
	type tlsConfig struct {
		InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty"`
		CaFile             string `yaml:"ca_file,omitempty"`
	}
	type scrapeConfig struct {
		JobName        string          `yaml:"job_name,omitempty"`
		ScrapeInterval string          `yaml:"scrape_interval,omitempty"`
		BearerToken    string          `yaml:"bearer_token,omitempty"`
		TlsConfig      tlsConfig       `yaml:"tls_config,omitempty"`
		Scheme         string          `yaml:"scheme,omitempty"`
		StaticConfigs  []staticConfigs `yaml:"static_configs,omitempty"`
	}
	type config struct {
		ScrapeConfigs []scrapeConfig `yaml:"scrape_configs,omitempty"`
	}

	err := func() error {
		scrapeConfigFile := filepath.Join(promCfgDir, ".prometheus-config.yaml")
		f, err := os.OpenFile(scrapeConfigFile, os.O_RDWR, 0o644)
		if os.IsNotExist(err) {
			return nil // Nothing to clean up
		}
		if err != nil {
			return err
		}
		defer f.Close()

		// lock config file exclusively
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != nil {
			return err
		}
		defer func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}()

		promCfg := config{}
		err = gopkgyaml.NewDecoder(f).Decode(&promCfg)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}

		// Remove scrape configs that match the job name prefix
		var filteredConfigs []scrapeConfig
		for _, cfg := range promCfg.ScrapeConfigs {
			// Check if CA file still exists - if not, remove the config
			if cfg.TlsConfig.CaFile != "" {
				if _, err := os.Stat(cfg.TlsConfig.CaFile); os.IsNotExist(err) {
					continue // Skip this config - CA file is gone
				}
			}
			filteredConfigs = append(filteredConfigs, cfg)
		}

		promCfg.ScrapeConfigs = filteredConfigs

		err = f.Truncate(0)
		if err != nil {
			return err
		}
		_, err = f.Seek(0, 0)
		if err != nil {
			return err
		}
		return gopkgyaml.NewEncoder(f).Encode(&promCfg)
	}()
	if err != nil {
		return err
	}

	// Reload Prometheus configuration
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, promUrl+"/-/reload", http.NoBody)
	if err != nil {
		return err
	}
	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
