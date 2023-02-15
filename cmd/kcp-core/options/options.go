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

package options

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
)

type Options struct {
	Output io.Writer

	Server serveroptions.Options
	Extra  ExtraOptions
}

type ExtraOptions struct {
	RootDirectory string
}

func NewOptions(rootDir string) *Options {
	opts := &Options{
		Output: nil,

		Server: *serveroptions.NewOptions(rootDir),
		Extra: ExtraOptions{
			RootDirectory: rootDir,
		},
	}

	return opts
}

type completedOptions struct {
	Output io.Writer

	Server serveroptions.CompletedOptions
	Extra  ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.Server.AddFlags(fss)

	fs := fss.FlagSet("KCP")
	fs.StringVar(&o.Extra.RootDirectory, "root-directory", o.Extra.RootDirectory, "Root directory.")
}

func (o *Options) Complete() (*CompletedOptions, error) {
	if !filepath.IsAbs(o.Extra.RootDirectory) {
		pwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Extra.RootDirectory = filepath.Join(pwd, o.Extra.RootDirectory)
	}

	// Create the configuration root correctly before other components get a chance.
	if err := mkdirRoot(o.Extra.RootDirectory); err != nil {
		return nil, err
	}

	server, err := o.Server.Complete(o.Extra.RootDirectory)
	if err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Output: o.Output,
			Server: *server,
		},
	}, nil
}

func (o *CompletedOptions) Validate() []error {
	errs := []error{}

	errs = append(errs, o.Server.Validate()...)

	return errs
}

// mkdirRoot creates the root configuration directory for the KCP
// server. This has to be done early before we start bringing up server
// components to ensure that we set the initial permissions correctly,
// since otherwise components will create it as a side-effect.
func mkdirRoot(dir string) error {
	if dir == "" {
		return errors.New("missing root directory configuration")
	}
	logger := klog.Background().WithValues("dir", dir)

	fi, err := os.Stat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}

		logger.Info("creating root directory")

		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}

		// Ensure the leaf directory is moderately private
		// because this may contain private keys and other
		// sensitive data
		return os.Chmod(dir, 0700)
	}

	if !fi.IsDir() {
		return fmt.Errorf("%q is a file, please delete or select another location", dir)
	}

	logger.Info("using root directory")
	return nil
}
