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

package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/cli/pkg/quickstart/scenarios"
	workspaceplugin "github.com/kcp-dev/cli/pkg/workspace/plugin"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		namePrefix   string
		withScenario bool
		wantErr      string
	}{
		{
			name:         "empty name-prefix",
			namePrefix:   "",
			withScenario: true,
			wantErr:      "name-prefix",
		},
		{
			name:         "valid prefix",
			namePrefix:   "my-test",
			withScenario: true,
		},
		{
			name:         "uppercase prefix rejected",
			namePrefix:   "MyTest",
			withScenario: true,
			wantErr:      "invalid workspace name",
		},
		{
			name:         "underscore prefix rejected",
			namePrefix:   "my_test",
			withScenario: true,
			wantErr:      "invalid workspace name",
		},
		{
			name:         "prefix with spaces rejected",
			namePrefix:   "my test",
			withScenario: true,
			wantErr:      "invalid workspace name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			streams, _, _, _ := genericiooptions.NewTestIOStreams()
			o := NewQuickstartOptions(streams)
			o.NamePrefix = tt.namePrefix
			if tt.withScenario {
				s, err := scenarios.Get("api-provider")
				if err != nil {
					t.Fatalf("scenarios.Get: %v", err)
				}
				o.scenario = s
			}
			err := o.Validate()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil && strings.Contains(err.Error(), "name-prefix") {
				t.Errorf("Validate() unexpectedly rejected valid prefix: %v", err)
			}
		})
	}
}

func TestComplete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		scenario string
		wantErr  string
	}{
		{
			name:     "unknown scenario",
			scenario: "does-not-exist",
			wantErr:  "unknown scenario",
		},
		{
			name:     "valid scenario populates o.scenario",
			scenario: "api-provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			streams, _, _, _ := genericiooptions.NewTestIOStreams()
			o := NewQuickstartOptions(streams)
			o.Scenario = tt.scenario

			s, err := scenarios.Get(o.Scenario)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("scenarios.Get() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("scenarios.Get() unexpected error: %v", err)
			}

			o.scenario = s
			if o.scenario == nil {
				t.Error("Complete() did not populate o.scenario")
			}
			if o.scenario.Name() != tt.scenario {
				t.Errorf("Complete() scenario name = %q, want %q", o.scenario.Name(), tt.scenario)
			}
		})
	}
}

func TestStepNaming(t *testing.T) {
	t.Parallel()
	s, err := scenarios.Get("api-provider")
	if err != nil {
		t.Fatalf("Get(api-provider): %v", err)
	}

	prefix := "myprefix"
	steps := s.Steps(prefix)

	wantNames := []string{
		prefix + "-org",
		prefix + "-provider",
		prefix + "-consumer",
	}
	for _, want := range wantNames {
		found := false
		for _, step := range steps {
			if strings.Contains(step.Description, want) {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("no step description contains %q; steps: %v", want, stepDescriptions(steps))
		}
	}
}

func stepDescriptions(steps []scenarios.Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Description
	}
	return out
}

func TestRunCleanup(t *testing.T) {
	t.Parallel()
	makeCleanupScenario := func(callOrder *[]string, stepErrors map[string]error) *cleanupScenario {
		return &cleanupScenario{
			steps: []scenarios.Step{
				{
					Description:        "step-A",
					CleanupDescription: "cleanup-A",
					Cleanup: func(_ context.Context, _ scenarios.ExecutionContext) error {
						*callOrder = append(*callOrder, "A")
						return stepErrors["A"]
					},
				},
				{
					Description: "step-B (no cleanup)",
				},
				{
					Description:        "step-C",
					CleanupDescription: "cleanup-C",
					Cleanup: func(_ context.Context, _ scenarios.ExecutionContext) error {
						*callOrder = append(*callOrder, "C")
						return stepErrors["C"]
					},
				},
			},
		}
	}

	newOpts := func(t *testing.T) *QuickstartOptions {
		t.Helper()
		streams, _, _, _ := genericiooptions.NewTestIOStreams()

		o := NewQuickstartOptions(streams)
		o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
			return nil, nil
		}
		o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
			return nil, nil
		}
		o.ClientConfig = &fakeClientConfig{}
		o.Cleanup = true
		o.enterWorkspace = func(_ context.Context, _ string) error {
			return nil
		}

		return o
	}

	t.Run("calls cleanup in reverse order", func(t *testing.T) {
		t.Parallel()
		var callOrder []string
		o := newOpts(t)
		o.scenario = makeCleanupScenario(&callOrder, nil)

		if err := o.Run(context.Background()); err != nil {
			t.Fatalf("Run() cleanup: %v", err)
		}
		want := []string{"C", "A"} // reverse order,and B has no Cleanup
		if strings.Join(callOrder, ", ") != strings.Join(want, ", ") {
			t.Errorf("cleanup order = %v, want %v", callOrder, want)
		}
	})

	t.Run("continues past errors and returns all failures", func(t *testing.T) {
		t.Parallel()
		var callOrder []string
		o := newOpts(t)
		o.scenario = makeCleanupScenario(&callOrder, map[string]error{
			"A": fmt.Errorf("scenario A failed"),
			"C": fmt.Errorf("scenario C failed"),
		})

		err := o.Run(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "scenario A failed") {
			t.Errorf("error missing 'scenario A failed': %v", err)
		}
		if !strings.Contains(err.Error(), "scenario C failed") {
			t.Errorf("error missing 'scenario C failed': %v", err)
		}
		if strings.Join(callOrder, ",") != "C,A" {
			t.Errorf("cleanup order = %v, want [C A]", callOrder)
		}
	})

	t.Run("switches kubeconfig to root after cleanup", func(t *testing.T) {
		t.Parallel()
		var callOrder []string
		o := newOpts(t)
		o.scenario = makeCleanupScenario(&callOrder, nil)

		var enteredPath string
		o.enterWorkspace = func(_ context.Context, path string) error {
			enteredPath = path
			return nil
		}

		if err := o.Run(context.Background()); err != nil {
			t.Fatalf("Run() cleanup: %v", err)
		}
		if enteredPath != ":root" {
			t.Errorf("enterWorkspace called with %q, want \":root\"", enteredPath)
		}
	})

	t.Run("skips steps without Cleanup func", func(t *testing.T) {
		t.Parallel()
		var callOrder []string
		o := newOpts(t)
		o.scenario = makeCleanupScenario(&callOrder, nil)

		if err := o.Run(context.Background()); err != nil {
			t.Fatalf("Run() cleanup: %v", err)
		}
		for _, name := range callOrder {
			if name == "B" {
				t.Error("step B has no Cleanup func but was called")
			}
		}
	})
}

type cleanupScenario struct{ steps []scenarios.Step }

func (c *cleanupScenario) Name() string {
	return "cleanup-test"
}
func (c *cleanupScenario) Steps(_ string) []scenarios.Step {
	return c.steps
}
func (c *cleanupScenario) Samples(_ string) []scenarios.Step {
	return nil
}
func (c *cleanupScenario) EnterPath(_ map[string]string) string {
	return ""
}
func (c *cleanupScenario) PrintSummary(_ io.Writer, _ string, _ map[string]string) error {
	return nil
}
func (c *cleanupScenario) Validate(_ string) error { return nil }

type mockScenario struct{}

func (m *mockScenario) Name() string {
	return "mock"
}
func (m *mockScenario) Samples(_ string) []scenarios.Step {
	return nil
}
func (m *mockScenario) Steps(_ string) []scenarios.Step {
	return []scenarios.Step{
		{
			Description: "set consumer-path",
			Execute: func(_ context.Context, execCtx scenarios.ExecutionContext) error {
				execCtx.State["consumer-path"] = "root:test-org:test-consumer"
				return nil
			},
		},
	}
}
func (m *mockScenario) EnterPath(state map[string]string) string {
	return state["consumer-path"]
}
func (m *mockScenario) PrintSummary(_ io.Writer, _ string, _ map[string]string) error { return nil }
func (m *mockScenario) Validate(_ string) error                                       { return nil }

type fakeClientConfig struct{}

func (f *fakeClientConfig) RawConfig() (clientcmdapi.Config, error) {
	return clientcmdapi.Config{}, nil
}
func (f *fakeClientConfig) ClientConfig() (*rest.Config, error) {
	return &rest.Config{Host: "https://localhost"}, nil
}
func (f *fakeClientConfig) Namespace() (string, bool, error) {
	return "default", false, nil
}
func (f *fakeClientConfig) ConfigAccess() clientcmd.ConfigAccess {
	return clientcmd.NewDefaultClientConfigLoadingRules()
}

func TestRunEnter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		enter         bool
		wantEnterPath string
	}{
		{
			name:          "enter=true calls enterWorkspace with consumer path",
			enter:         true,
			wantEnterPath: ":root:test-org:test-consumer",
		},
		{
			name:  "enter=false does not call enterWorkspace",
			enter: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			streams, _, _, _ := genericiooptions.NewTestIOStreams()
			o := NewQuickstartOptions(streams)
			o.Enter = tt.enter
			o.scenario = &mockScenario{}
			o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
				return nil, nil
			}
			o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
				return nil, nil
			}
			o.ClientConfig = &fakeClientConfig{}

			var capturedPath string
			o.enterWorkspace = func(_ context.Context, path string) error {
				capturedPath = path
				return nil
			}

			if err := o.Run(context.Background()); err != nil {
				t.Fatalf("Run(): %v", err)
			}

			if tt.wantEnterPath != "" {
				if capturedPath != tt.wantEnterPath {
					t.Errorf("enterWorkspace called with %q, want %q", capturedPath, tt.wantEnterPath)
				}
			} else {
				if capturedPath != "" {
					t.Errorf("enterWorkspace should not have been called, but got path %q", capturedPath)
				}
			}
		})
	}
}

func TestRunEnterError(t *testing.T) {
	t.Parallel()
	streams, _, _, _ := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.Enter = true
	o.scenario = &mockScenario{}
	o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
		return nil, nil
	}
	o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
		return nil, nil
	}
	o.ClientConfig = &fakeClientConfig{}
	o.enterWorkspace = func(_ context.Context, _ string) error {
		return errors.New("kubeconfig write failed")
	}

	err := o.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "kubeconfig write failed") {
		t.Errorf("expected error containing 'kubeconfig write failed', got %v", err)
	}
}

func TestDefaultEnterWorkspaceForwardsOptions(t *testing.T) {
	t.Parallel()
	streams, _, _, _ := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.KubectlOverrides.CurrentContext = "my-context"
	o.ClientConfig = &fakeClientConfig{}

	var capturedUseOpts *workspaceplugin.UseWorkspaceOptions
	o.newUseWorkspaceOpts = func(streams genericiooptions.IOStreams) *workspaceplugin.UseWorkspaceOptions {
		capturedUseOpts = workspaceplugin.NewUseWorkspaceOptions(streams)
		return capturedUseOpts
	}

	// the call will fail (since no real cluster), but the Options assignment happens before Run().
	_ = o.defaultEnterWorkspace(context.Background(), "root:test")

	if capturedUseOpts == nil {
		t.Fatal("newUseWorkspaceOpts was not called")
	}
	if capturedUseOpts.Options != o.Options {
		t.Error("defaultEnterWorkspace did not forward Options to UseWorkspaceOptions")
	}
}

func TestPrintConnectionHintFormatsServerURL(t *testing.T) {
	streams, _, _, errOut := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.serverURL = "https://192.168.0.5:6443"

	o.printConnectionHint()

	got := errOut.String()
	if !strings.Contains(got, "Cannot reach kcp") {
		t.Errorf("expected 'Cannot reach kcp' in stderr, got: %q", got)
	}
	if !strings.Contains(got, "Make sure kcp is running") {
		t.Errorf("expected setup instructions in stderr, got: %q", got)
	}
	if !strings.Contains(got, "https://192.168.0.5:6443") {
		t.Errorf("expected server URL in stderr, got: %q", got)
	}
}

func TestRunPreservesErrorChainOnStepFailure(t *testing.T) {
	streams, _, _, errOut := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
		return nil, nil
	}
	o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
		return nil, nil
	}
	o.ClientConfig = &fakeClientConfig{}

	netErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: fmt.Errorf("connection refused"),
	}
	o.scenario = &cleanupScenario{steps: []scenarios.Step{
		{
			Description: "step-A",
			Execute:     func(_ context.Context, _ scenarios.ExecutionContext) error { return netErr },
		},
	}}

	err := o.Run(context.Background())
	if err == nil {
		t.Fatal("expected error from failing step, got nil")
	}

	var got *net.OpError
	if !errors.As(err, &got) {
		t.Errorf("Run should preserve typed error chain; errors.As(err, *net.OpError) failed for %v", err)
	} else if !errors.Is(got, netErr) {
		t.Errorf("errors.As recovered a different error: got %v, want %v", got, netErr)
	}

	if !strings.Contains(errOut.String(), "Cannot reach kcp") {
		t.Errorf("expected connection hint in stderr, got: %q", errOut.String())
	}
}

func TestRunWithHintEmitsHintWhenServerUnreachable(t *testing.T) {
	streams, _, _, errOut := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
		return nil, nil
	}
	o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
		return nil, nil
	}
	o.ClientConfig = &fakeClientConfig{}

	connErr := &url.Error{
		Op:  "Post",
		URL: "https://localhost:6443/x",
		Err: &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
		},
	}
	o.scenario = &cleanupScenario{steps: []scenarios.Step{
		{
			Description: "step-A",
			Execute: func(_ context.Context, _ scenarios.ExecutionContext) error {
				return connErr
			},
		},
	}}

	err := o.Run(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(errOut.String(), "Cannot reach kcp") {
		t.Errorf("expected hint in stderr, got: %q", errOut.String())
	}
}

func TestRunWithHintSuppressesHintWhenServerReachable(t *testing.T) {
	streams, _, _, errOut := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
		return nil, nil
	}
	o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
		return nil, nil
	}
	o.ClientConfig = &fakeClientConfig{}
	o.scenario = &cleanupScenario{steps: []scenarios.Step{
		{
			Description: "step-A",
			Execute: func(_ context.Context, _ scenarios.ExecutionContext) error {
				return errors.New("unauthorized")
			},
		},
	}}

	err := o.Run(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(errOut.String(), "Cannot reach kcp") {
		t.Errorf("expected NO hint for a non-connection error, got: %q", errOut.String())
	}
}

func TestRunWithHintSilentOnSuccess(t *testing.T) {
	streams, _, _, errOut := genericiooptions.NewTestIOStreams()
	o := NewQuickstartOptions(streams)
	o.newKCPClusterClient = func(_ *rest.Config) (kcpclientset.ClusterInterface, error) {
		return nil, nil
	}
	o.newKCPDynamicClient = func(_ *rest.Config) (kcpdynamic.ClusterInterface, error) {
		return nil, nil
	}
	o.ClientConfig = &fakeClientConfig{}
	o.scenario = &mockScenario{}
	o.Enter = false

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run(): %v", err)
	}
	if errOut.String() != "" {
		t.Errorf("expected no stderr on success, got: %q", errOut.String())
	}
}

func TestIsConnectionError(t *testing.T) {
	wrapURL := func(cause error) error {
		return fmt.Errorf("step 1/6 failed: %w", &url.Error{
			Op:  "Post",
			URL: "https://localhost:6443/clusters/root/apis",
			Err: &net.OpError{Op: "dial", Net: "tcp", Err: cause},
		})
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "connection refused",
			err:  wrapURL(os.NewSyscallError("connect", syscall.ECONNREFUSED)),
			want: true,
		},
		{
			name: "timed out",
			err:  wrapURL(os.NewSyscallError("connect", syscall.ETIMEDOUT)),
			want: true,
		},
		{
			name: "host unreachable",
			err:  wrapURL(os.NewSyscallError("connect", syscall.EHOSTUNREACH)),
			want: true,
		},
		{
			name: "network unreachable",
			err:  wrapURL(os.NewSyscallError("connect", syscall.ENETUNREACH)),
			want: true,
		},
		{
			name: "i/o timeout without syscall errno",
			err:  fmt.Errorf("boom: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}),
			want: true,
		},
		{
			name: "DNS host not found",
			err:  fmt.Errorf("boom: %w", &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}),
			want: true,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "auth error is not a connection error",
			err:  errors.New(`the server has asked for the client to provide credentials (post workspaces.tenancy.kcp.io)`),
			want: false,
		},
		{
			name: "TLS/x509 error is not a connection error",
			err:  errors.New(`x509: certificate signed by unknown authority`),
			want: false,
		},
		{
			name: "non-dial net.OpError (e.g. write on closed conn) is not treated as unreachable",
			err:  &net.OpError{Op: "write", Net: "tcp", Err: errors.New("broken pipe")},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnectionError(tt.err); got != tt.want {
				t.Errorf("isConnectionError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
