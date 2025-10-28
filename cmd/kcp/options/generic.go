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
	"fmt"
	"os"
	"path/filepath"

	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"
)

type GenericOptions struct {
	RootDirectory string
	MappingFile   string
}

func NewGeneric(rootDir string) *GenericOptions {
	return &GenericOptions{
		RootDirectory: rootDir,
		MappingFile:   "",
	}
}

func (o *GenericOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("kcp")
	fs.StringVar(&o.RootDirectory, "root-directory", o.RootDirectory, "Root directory. Set to \"\" to disable file (e.g. certificates) generation in a root directory.")
	fs.StringVar(&o.MappingFile, "miniproxy-mapping-file", o.MappingFile, "DEVELOPMENT ONLY. Path to additional mapping file to be used by mini-front-proxy. This should not be used in production. For production usecase use front-proxy component instead.")
}

func (o *GenericOptions) Complete() (*GenericOptions, error) {
	if o.RootDirectory != "" {
		if !filepath.IsAbs(o.RootDirectory) {
			pwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			o.RootDirectory = filepath.Join(pwd, o.RootDirectory)
		}

		// Create the configuration root correctly before other components get a chance.
		if err := mkdirRoot(o.RootDirectory); err != nil {
			return nil, err
		}
	}
	if o.MappingFile != "" {
		if !filepath.IsAbs(o.MappingFile) {
			pwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			o.MappingFile = filepath.Join(pwd, o.MappingFile)
		}
	}

	return o, nil
}

func (o *GenericOptions) Validate() []error {
	return nil
}

// mkdirRoot creates the root configuration directory for the kcp
// server. This has to be done early before we start bringing up server
// components to ensure that we set the initial permissions correctly,
// since otherwise components will create it as a side-effect.
func mkdirRoot(dir string) error {
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
