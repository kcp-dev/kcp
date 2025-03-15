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

// Config qualify a kcp server to start
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type Config struct {
	Name        string
	Args        []string
	ArtifactDir string
	DataDir     string
	ClientCADir string

	LogToConsole bool
	RunInProcess bool
}

// Option a function that wish to modify a given kcp configuration.
type Option func(*Config) *Config

// WithScratchDirectories adds custom scratch directories to a kcp configuration.
func WithScratchDirectories(artifactDir, dataDir string) Option {
	return func(cfg *Config) *Config {
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
		return cfg
	}
}

// WithCustomArguments applies provided arguments to a given kcp configuration.
func WithCustomArguments(args ...string) Option {
	return func(cfg *Config) *Config {
		cfg.Args = args
		return cfg
	}
}

// WithClientCADir sets the client CA directory for a given kcp configuration.
func WithClientCADir(clientCADir string) Option {
	return func(cfg *Config) *Config {
		cfg.ClientCADir = clientCADir
		return cfg
	}
}
