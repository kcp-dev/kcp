/*
Copyright 2021 The KCP Authors.

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

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egymgmbh/go-prefix-writer/prefixer"

	"k8s.io/apimachinery/pkg/util/wait"
)

// NewAccessory creates a new accessory process.
func NewAccessory(t *testing.T, artifactDir string, name string, cmd ...string) *Accessory {
	return &Accessory{
		t:           t,
		artifactDir: artifactDir,
		name:        name,
		cmd:         cmd,
	}
}

// Accessory knows how to run an executable with arguments for the duration of the context.
type Accessory struct {
	ctx         context.Context
	t           *testing.T
	artifactDir string
	name        string
	cmd         []string
}

func (a *Accessory) Run(t *testing.T, opts ...RunOption) error {
	runOpts := runOptions{}
	for _, opt := range opts {
		opt(&runOpts)
	}
	if runOpts.runInProcess {
		return fmt.Errorf("cannot run arbitrary accessories in process")
	}

	ctx, cleanupCancel := context.WithCancel(context.Background())
	a.t.Cleanup(func() {
		a.t.Logf("cleanup: ending `%s`", a.name)
		cleanupCancel()
		<-ctx.Done()
	})

	a.ctx = ctx
	cmd := exec.CommandContext(ctx, a.cmd[0], a.cmd[1:]...)

	a.t.Logf("running: %v", strings.Join(cmd.Args, " "))
	logFile, err := os.Create(filepath.Join(a.artifactDir, fmt.Sprintf("%s.log", a.name)))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create log file: %w", err)
	}
	log := bytes.Buffer{}
	writers := []io.Writer{&log, logFile}
	if runOpts.streamLogs {
		prefix := fmt.Sprintf("%s: ", a.name)
		writers = append(writers, prefixer.New(os.Stdout, func() string { return prefix }))
	}
	mw := io.MultiWriter(writers...)
	cmd.Stdout = mw
	cmd.Stderr = mw
	if err := cmd.Start(); err != nil {
		cleanupCancel()
		return err
	}
	go func() {
		defer cleanupCancel()
		err := cmd.Wait()
		if err != nil && ctx.Err() == nil {
			a.t.Errorf("`%s` failed: %v output: %s", a.name, err, log.String())
		}
	}()
	return nil
}

// Ready blocks until the server is healthy and ready.
func Ready(ctx context.Context, t *testing.T, port string) bool {
	wg := sync.WaitGroup{}
	wg.Add(2)
	for _, endpoint := range []string{"/healthz", "/readyz"} {
		go func(endpoint string) {
			defer wg.Done()
			waitForEndpoint(ctx, t, port, endpoint)
		}(endpoint)
	}
	wg.Wait()
	return !t.Failed()
}

func waitForEndpoint(ctx context.Context, t *testing.T, port, endpoint string) {
	var lastError error
	if err := wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(ctx context.Context) (bool, error) {
		url := fmt.Sprintf("http://[::1]:%s%s", port, endpoint)
		resp, err := http.Get(url) //nolint:noctx
		if err != nil {
			lastError = fmt.Errorf("error contacting %s: %w", url, err)
			return false, nil
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastError = fmt.Errorf("error reading response from %s: %w", url, err)
			return false, nil
		}
		if resp.StatusCode != 200 {
			lastError = fmt.Errorf("unready response from %s: %v", url, string(body))
			return false, nil
		}

		t.Logf("success contacting %s", url)
		return true, nil
	}); err != nil && lastError != nil {
		t.Error(lastError)
	}
}
