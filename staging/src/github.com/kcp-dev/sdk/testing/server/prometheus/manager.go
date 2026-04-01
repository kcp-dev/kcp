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

package prometheus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"syscall"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ScrapeConfigs []ScrapeConfig `yaml:"scrape_configs,omitempty"`
}

type ScrapeConfig struct {
	JobName        string         `yaml:"job_name,omitempty"`
	ScrapeInterval string         `yaml:"scrape_interval,omitempty"`
	BearerToken    string         `yaml:"bearer_token,omitempty"` //nolint:gosec // Field has to be exported for the decoder.
	TlsConfig      TLSConfig      `yaml:"tls_config,omitempty"`
	Scheme         string         `yaml:"scheme,omitempty"`
	StaticConfigs  []StaticConfig `yaml:"static_configs,omitempty"`
}

type TLSConfig struct {
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty"`
	CaFile             string `yaml:"ca_file,omitempty"`
}

type StaticConfig struct {
	Targets []string          `yaml:"targets,omitempty"`
	Labels  map[string]string `yaml:"labels,omitempty"`
}

func EnsureScrapeConfig(configFile string, sc ScrapeConfig) error {
	return patchConfigFile(configFile, func(cfg *Config) error {
		for idx, existingSC := range cfg.ScrapeConfigs {
			if existingSC.JobName == sc.JobName {
				cfg.ScrapeConfigs[idx] = sc
				return nil
			}
		}

		cfg.ScrapeConfigs = append(cfg.ScrapeConfigs, sc)
		return nil
	})
}

func RemoveScrapeConfig(configFile string, jobName string) error {
	return patchConfigFile(configFile, func(cfg *Config) error {
		cfg.ScrapeConfigs = slices.DeleteFunc(cfg.ScrapeConfigs, func(sc ScrapeConfig) bool {
			return sc.JobName == jobName
		})
		return nil
	})
}

func Reload(ctx context.Context, promUrl string) error {
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

func patchConfigFile(configFile string, patch func(*Config) error) error {
	f, err := os.OpenFile(configFile, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	fd := int(f.Fd()) //nolint:gosec // the conversion uintpr -> int is fine here
	// lock config file exclusively, blocks all other producers until unlocked or process (test) exits
	err = syscall.Flock(fd, syscall.LOCK_EX)
	if err != nil {
		return err
	}

	promCfg := Config{}
	err = yaml.NewDecoder(f).Decode(&promCfg)
	// ignore EOF because the configFile initially is entirely empty
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	if err := patch(&promCfg); err != nil {
		return err
	}

	err = f.Truncate(0)
	if err != nil {
		return err
	}

	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}

	encoder := yaml.NewEncoder(f)
	encoder.SetIndent(2)

	err = encoder.Encode(&promCfg)
	if err != nil {
		return err
	}

	return syscall.Flock(fd, syscall.LOCK_UN)
}
