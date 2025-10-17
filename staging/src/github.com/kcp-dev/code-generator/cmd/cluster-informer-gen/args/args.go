/*
Copyright 2025 The Kubernetes Authors.
Modifications Copyright 2025 The KCP Authors.

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

package args

import (
	"fmt"

	"github.com/spf13/pflag"
)

// Args is used by the gengo framework to pass args specific to this generator.
type Args struct {
	OutputDir                 string // must be a directory path
	OutputPackage             string // must be a Go import-path
	GoHeaderFile              string
	VersionedClientSetPackage string // must be a Go import-path
	ListersPackage            string // must be a Go import-path

	// Path to the generated Kubernetes single-cluster versioned clientset package.
	SingleClusterVersionedClientSetPackage string
	// Path to the generated Kubernetes single-cluster informers package.
	SingleClusterInformersPackage string
	// Path to the generated Kubernetes single-cluster listers package.
	SingleClusterListersPackage string

	// PluralExceptions define a list of pluralizer exceptions in Type:PluralType format.
	// The default list is "Endpoints:Endpoints"
	PluralExceptions []string
}

// New returns default arguments for the generator.
func New() *Args {
	return &Args{}
}

// AddFlags add the generator flags to the flag set.
func (args *Args) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&args.OutputDir, "output-dir", "",
		"the base directory under which to generate results")
	fs.StringVar(&args.OutputPackage, "output-pkg", args.OutputPackage,
		"the Go import-path of the generated results")
	fs.StringVar(&args.GoHeaderFile, "go-header-file", "",
		"the path to a file containing boilerplate header text; the string \"YEAR\" will be replaced with the current 4-digit year")
	fs.StringVar(&args.VersionedClientSetPackage, "versioned-clientset-pkg", args.VersionedClientSetPackage,
		"the Go import-path of the versioned clientset to use")
	fs.StringVar(&args.ListersPackage, "listers-pkg", args.ListersPackage,
		"the Go import-path of the listers to use")
	fs.StringVar(&args.SingleClusterVersionedClientSetPackage, "single-cluster-versioned-clientset-pkg", args.SingleClusterVersionedClientSetPackage,
		"package path to the generated Kubernetes single-cluster versioned clientset package")
	fs.StringVar(&args.SingleClusterInformersPackage, "single-cluster-informers-pkg", args.SingleClusterInformersPackage,
		"package path to the generated Kubernetes single-cluster informers package (optional)")
	fs.StringVar(&args.SingleClusterListersPackage, "single-cluster-listers-pkg", args.SingleClusterListersPackage,
		"package path to the generated Kubernetes single-cluster listers package (optional)")
	fs.StringSliceVar(&args.PluralExceptions, "plural-exceptions", args.PluralExceptions,
		"list of comma separated plural exception definitions in Type:PluralizedType format")
}

// Validate checks the given arguments.
func (args *Args) Validate() error {
	if len(args.OutputDir) == 0 {
		return fmt.Errorf("--output-dir must be specified")
	}
	if len(args.OutputPackage) == 0 {
		return fmt.Errorf("--output-pkg must be specified")
	}
	if len(args.VersionedClientSetPackage) == 0 {
		return fmt.Errorf("--versioned-clientset-pkg must be specified")
	}
	if len(args.ListersPackage) == 0 {
		return fmt.Errorf("--listers-pkg must be specified")
	}
	if len(args.SingleClusterVersionedClientSetPackage) == 0 {
		return fmt.Errorf("--single-cluster-versioned-clientset-pkg must be specified")
	}
	return nil
}
