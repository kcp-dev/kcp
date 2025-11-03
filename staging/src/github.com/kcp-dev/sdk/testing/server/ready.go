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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// WaitForReady waits for /livez and then /readyz to return success.
func WaitForReady(ctx context.Context, cfg *rest.Config) error {
	cfg = rest.CopyConfig(cfg)
	if cfg.NegotiatedSerializer == nil {
		cfg.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
	}

	client, err := rest.UnversionedRESTClientFor(cfg)
	if err != nil {
		return fmt.Errorf("failed to create unversioned client: %w", err)
	}

	if err := waitForEndpoint(ctx, client, "/livez"); err != nil {
		return fmt.Errorf("server at %s didn't become ready: %w", cfg.Host, err)
	}
	if err := waitForEndpoint(ctx, client, "/readyz"); err != nil {
		return fmt.Errorf("server at %s didn't become ready: %w", cfg.Host, err)
	}

	return nil
}

func waitForEndpoint(ctx context.Context, client *rest.RESTClient, endpoint string) error {
	var lastError error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		req := rest.NewRequest(client).RequestURI(endpoint)
		if _, err := req.Do(ctx).Raw(); err != nil {
			lastError = fmt.Errorf("error contacting %s: failed components: %v", req.URL(), unreadyComponentsFromError(err))
			return false, nil
		}
		return true, nil
	}); err != nil && lastError != nil {
		return lastError
	}
	return nil
}

// MonitorEndpoints keeps watching the given endpoints and fails t on error.
func MonitorEndpoints(t TestingT, client *rest.Config, endpoints ...string) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, endpoint := range endpoints {
		go func(endpoint string) {
			monitorEndpoint(ctx, t, client, endpoint)
		}(endpoint)
	}
}

func monitorEndpoint(ctx context.Context, t TestingT, cfg *rest.Config, endpoint string) {
	cfg = rest.CopyConfig(cfg)
	if cfg.NegotiatedSerializer == nil {
		cfg.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
	}
	client, err := rest.UnversionedRESTClientFor(cfg)
	if err != nil {
		t.Errorf("failed to create unversioned client: %v", err)
		return
	}

	var errCount int
	errs := sets.New[string]()
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		_, err := rest.NewRequest(client).RequestURI(endpoint).Do(ctx).Raw()
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		// if we're noticing an error, record it and fail the test if things stay failed for two consecutive polls
		if err != nil {
			errCount++
			errs.Insert(fmt.Sprintf("failed components: %v", unreadyComponentsFromError(err)))
			if errCount == 2 {
				select {
				case <-ctx.Done():
					// ignore error if we're already shutting down
				default:
					if !t.Failed() {
						t.Errorf("error contacting %s: %v", endpoint, sets.List[string](errs))
					}
				}
			}
		}
		// otherwise, reset the counters
		errCount = 0
		if errs.Len() > 0 {
			errs = sets.New[string]()
		}
	}, 100*time.Millisecond)
}

// there doesn't seem to be any simple way to get a metav1.Status from the Go client, so we get
// the content in a string-formatted error, unfortunately.
func unreadyComponentsFromError(err error) string {
	innerErr := strings.TrimPrefix(strings.TrimSuffix(err.Error(), `") has prevented the request from succeeding`), `an error on the server ("`)
	var unreadyComponents []string
	for _, line := range strings.Split(innerErr, `\n`) {
		if name := strings.TrimPrefix(strings.TrimSuffix(line, ` failed: reason withheld`), `[-]`); name != line {
			// NB: sometimes the error we get is truncated (server-side?) to something like: `\n[-]poststar") has prevented the request from succeeding`
			// In those cases, the `name` here is also truncated, but nothing we can do about that. For that reason, we don't expose a list of components
			// from this function or else we'd need to handle more edge cases.
			unreadyComponents = append(unreadyComponents, name)
		}
	}
	return strings.Join(unreadyComponents, ", ")
}
