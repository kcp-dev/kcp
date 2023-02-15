/*
Copyright 2023 The KCP Authors.

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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/validation/field"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type StartPlaygroundOptions struct {
	genericclioptions.IOStreams

	SpecPath string

	Spec *PlaygroundSpec
}

func NewStartPlaygroundOptions(streams genericclioptions.IOStreams) *StartPlaygroundOptions {
	return &StartPlaygroundOptions{
		IOStreams: streams,
		Spec:      &PlaygroundSpec{},
	}
}

func (o *StartPlaygroundOptions) Complete(_ []string) error {
	return o.Spec.CompleteFromFile(o.SpecPath)
}

func (o *StartPlaygroundOptions) Validate() error {
	return o.Spec.Validate(field.NewPath("<config>"))
}

func (o *StartPlaygroundOptions) BindFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.SpecPath, "file", "f", defaultSpecPath, "Path to a playground spec file.")
}

func (o *StartPlaygroundOptions) Run(_ context.Context) error {
	// NOTE: we are running this as a test, so we can leverage on the KCP test framework
	// no matter of the plugin isn't started using go test.
	// Please note that while running code as a test, t.Log does not work because -test.v
	// is not set, but that's ok because for the CLI plugin we want to surface only
	// a few messages, so we use a prettyPrinter writing to stderr.
	var allTestsFilter = func(_, _ string) (bool, error) { return true, nil }
	testing.Main(allTestsFilter, []testing.InternalTest{
		{
			Name: "start playground",
			F:    o.doStart,
		},
	}, nil, nil)
	return nil
}

func (o *StartPlaygroundOptions) doStart(t *testing.T) {
	t.Helper()

	o.prettyPrint("Starting KCP playground 🛝...\n")

	playground := NewPlaygroundFixture(t, o.Spec,
		WithPlaygroundRootDirectory(defaultRootDirectory),
		WithPrettyPrinter(o.prettyPrint),
	).Start(t)

	o.prettyPrint("\nKCP playground successfully started 🎈🥳🎈!\n")
	o.prettyPrint("To start using the playground:\n")
	o.prettyPrint(" ▶️  export KUBECONFIG=%s\n", playground.KubeConfigPath())

	o.prettyPrint("\nThis will point to `main` shard, `root`; to change your context you can use:\n")
	o.prettyPrint(" ⏩️ kubectl kcp playground use shard [%s]\n", strings.Join(playground.Spec.ShardNames(), "|"))
	o.prettyPrint(" ⏩️ kubectl kcp playground use pcluster [%s]\n", strings.Join(playground.Spec.PClusterNames(), "|"))

	o.prettyPrint("\n ⏹️  Press CTRL + C to stop the playground\n")

	// create a lock file tobe used by `kcp playground use` to check if the playground is running.
	if err := createLockFor(startLockFile()); err != nil {
		t.Fatalf("failed to lock '%s': %v", startLockFile(), err)
	}
	defer func() {
		_ = deleteLockFor(startLockFile())
	}()

	ctx := genericapiserver.SetupSignalContext()
	<-ctx.Done()

	o.prettyPrint("\nBye!\n")
}

func (o *StartPlaygroundOptions) prettyPrint(format string, args ...any) {
	_, _ = fmt.Fprintf(o.ErrOut, format, args...)
}

func startLockFile() string {
	// NOTE: this works because the plugin is hard coded to use defaultRootDirectory as a root directory.
	return filepath.Join(defaultRootDirectory, defaultSpecPath)
}

func startIsRunning() bool {
	if _, err := os.Stat(lockNameFor(startLockFile())); err != nil {
		return false
	}
	return true
}
