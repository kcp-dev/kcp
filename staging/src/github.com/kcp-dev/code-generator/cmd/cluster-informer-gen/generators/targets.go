/*
Copyright 2025 The KCP Authors.
Copyright 2025 The Kubernetes Authors.

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

package generators

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/gengo/v2"
	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
	"github.com/kcp-dev/code-generator/v3/cmd/cluster-informer-gen/args"
	"github.com/kcp-dev/code-generator/v3/pkg/imports"
	genutil "github.com/kcp-dev/code-generator/v3/pkg/util"
)

// NameSystems returns the name system used by the generators in this package.
func NameSystems(pluralExceptions map[string]string) namer.NameSystems {
	return namer.NameSystems{
		"public":             namer.NewPublicNamer(0),
		"private":            namer.NewPrivateNamer(0),
		"raw":                namer.NewRawNamer("", nil),
		"publicPlural":       namer.NewPublicPluralNamer(pluralExceptions),
		"allLowercasePlural": namer.NewAllLowercasePluralNamer(pluralExceptions),
		"lowercaseSingular":  &lowercaseSingularNamer{},
	}
}

// lowercaseSingularNamer implements Namer.
type lowercaseSingularNamer struct{}

// Name returns t's name in all lowercase.
func (n *lowercaseSingularNamer) Name(t *types.Type) string {
	return strings.ToLower(t.Name.Name)
}

// DefaultNameSystem returns the default name system for ordering the types to be
// processed by the generators in this package.
func DefaultNameSystem() string {
	return "public"
}

// objectMetaForPackage returns the type of ObjectMeta used by package p.
func objectMetaForPackage(pkg *types.Package) (*types.Type, bool, error) {
	generate := false
	for _, t := range pkg.Types {
		if !util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...)).GenerateClient {
			continue
		}
		generate = true
		for _, member := range t.Members {
			if member.Name == "ObjectMeta" {
				return member.Type, isInternal(member), nil
			}
		}
	}
	if generate {
		return nil, false, fmt.Errorf("unable to find ObjectMeta for any types in package %s", pkg.Path)
	}
	return nil, false, nil
}

// isInternal returns true if the tags for a member do not contain a json tag.
func isInternal(m types.Member) bool {
	return !strings.Contains(m.Tags, "json")
}

const subdirForInternalInterfaces = "internalinterfaces"

// GetTargets makes the client target definition.
func GetTargets(context *generator.Context, args *args.Args) []generator.Target {
	boilerplate, err := gengo.GoBoilerplate(args.GoHeaderFile, "", gengo.StdGeneratedBy)
	if err != nil {
		klog.Fatalf("Failed loading boilerplate: %v", err)
	}

	externalVersionOutputDir := args.OutputDir
	externalVersionOutputPkg := args.OutputPackage

	targetList := make([]generator.Target, 0)
	typesForGroupVersion := make(map[clientgentypes.GroupVersion][]*types.Type)

	externalGroupVersions := make(map[string]clientgentypes.GroupVersions)
	groupGoNames := make(map[string]string)
	for _, inputPkg := range context.Inputs {
		pkg := context.Universe.Package(inputPkg)

		objectMeta, _, err := objectMetaForPackage(pkg)
		if err != nil {
			klog.Fatal(err)
		}
		if objectMeta == nil {
			// no types in this package had genclient
			continue
		}

		var (
			gv                  clientgentypes.GroupVersion
			targetGroupVersions map[string]clientgentypes.GroupVersions
		)

		gv.Group = clientgentypes.Group(path.Base(path.Dir(pkg.Path)))
		gv.Version = clientgentypes.Version(path.Base(pkg.Path))
		targetGroupVersions = externalGroupVersions

		groupPkgName := gv.Group.NonEmpty()
		gvPkg := path.Clean(pkg.Path)

		// If there's a comment of the form "// +groupName=somegroup" or
		// "// +groupName=somegroup.foo.bar.io", use the first field (somegroup) as the name of the
		// group when generating.
		if override := gengo.ExtractCommentTags("+", pkg.Comments)["groupName"]; override != nil { //nolint:staticcheck
			gv.Group = clientgentypes.Group(override[0])
		}

		// If there's a comment of the form "// +groupGoName=SomeUniqueShortName", use that as
		// the Go group identifier in CamelCase. It defaults
		groupGoNames[groupPkgName] = namer.IC(strings.Split(gv.Group.NonEmpty(), ".")[0])
		if override := gengo.ExtractCommentTags("+", pkg.Comments)["groupGoName"]; override != nil { //nolint:staticcheck
			groupGoNames[groupPkgName] = namer.IC(override[0])
		}

		var typesToGenerate []*types.Type
		for _, t := range pkg.Types {
			tags := util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
			if !tags.GenerateClient || tags.NoVerbs || !tags.HasVerb("list") || !tags.HasVerb("watch") {
				continue
			}

			typesToGenerate = append(typesToGenerate, t)
			typesForGroupVersion[gv] = append(typesForGroupVersion[gv], t)
		}
		if len(typesToGenerate) == 0 {
			continue
		}

		groupVersionsEntry, ok := targetGroupVersions[groupPkgName]
		if !ok {
			groupVersionsEntry = clientgentypes.GroupVersions{
				PackageName: groupPkgName,
				Group:       gv.Group,
			}
		}
		groupVersionsEntry.Versions = append(groupVersionsEntry.Versions, clientgentypes.PackageVersion{Version: gv.Version, Package: gvPkg})
		targetGroupVersions[groupPkgName] = groupVersionsEntry

		orderer := namer.Orderer{Namer: namer.NewPrivateNamer(0)}
		typesToGenerate = orderer.OrderTypes(typesToGenerate)

		outputDirBase := externalVersionOutputDir
		outputPkgBase := externalVersionOutputPkg
		clientSetPkg := args.VersionedClientSetPackage

		targetList = append(targetList, versionTarget(outputDirBase, outputPkgBase, groupPkgName, gv, groupGoNames[groupPkgName], boilerplate, typesToGenerate, clientSetPkg, args))
	}

	if len(externalGroupVersions) != 0 {
		targetList = append(targetList,
			factoryInterfaceTarget(externalVersionOutputDir, externalVersionOutputPkg, boilerplate, args),
			factoryTarget(externalVersionOutputDir, externalVersionOutputPkg, boilerplate, groupGoNames, genutil.PluralExceptionListToMapOrDie(args.PluralExceptions), externalGroupVersions, typesForGroupVersion, args),
		)

		for _, gvs := range externalGroupVersions {
			targetList = append(targetList, groupTarget(externalVersionOutputDir, externalVersionOutputPkg, gvs, boilerplate, args))
		}
	}

	return targetList
}

func factoryTarget(
	outputDirBase, outputPkgBase string, boilerplate []byte, groupGoNames, pluralExceptions map[string]string,
	groupVersions map[string]clientgentypes.GroupVersions, typesForGroupVersion map[clientgentypes.GroupVersion][]*types.Type,
	args *args.Args,
) generator.Target {
	return &generator.SimpleTarget{
		PkgName:       path.Base(outputDirBase),
		PkgPath:       outputPkgBase,
		PkgDir:        outputDirBase,
		HeaderComment: boilerplate,
		GeneratorsFunc: func(_ *generator.Context) (generators []generator.Generator) {
			generators = append(generators,
				&factoryGenerator{
					GoGenerator: generator.GoGenerator{
						OutputFilename: "factory.go",
					},
					outputPackage:                          outputPkgBase,
					imports:                                imports.NewImportTrackerForPackage(outputPkgBase),
					groupVersions:                          groupVersions,
					clientSetPackage:                       args.VersionedClientSetPackage,
					internalInterfacesPackage:              path.Join(outputPkgBase, subdirForInternalInterfaces),
					gvGoNames:                              groupGoNames,
					singleClusterVersionedClientSetPackage: args.SingleClusterVersionedClientSetPackage,
					singleClusterInformersPackage:          args.SingleClusterInformersPackage,
				},
				&genericGenerator{
					GoGenerator: generator.GoGenerator{
						OutputFilename: "generic.go",
					},
					outputPackage:             outputPkgBase,
					imports:                   imports.NewImportTrackerForPackage(outputPkgBase),
					groupVersions:             groupVersions,
					pluralExceptions:          pluralExceptions,
					typesForGroupVersion:      typesForGroupVersion,
					groupGoNames:              groupGoNames,
					singleClusterInformersPkg: args.SingleClusterInformersPackage,
				})

			return generators
		},
	}
}

func factoryInterfaceTarget(outputDirBase, outputPkgBase string, boilerplate []byte, args *args.Args) generator.Target {
	outputDir := filepath.Join(outputDirBase, subdirForInternalInterfaces)
	outputPkg := path.Join(outputPkgBase, subdirForInternalInterfaces)

	return &generator.SimpleTarget{
		PkgName:       path.Base(outputDir),
		PkgPath:       outputPkg,
		PkgDir:        outputDir,
		HeaderComment: boilerplate,
		GeneratorsFunc: func(_ *generator.Context) (generators []generator.Generator) {
			generators = append(generators, &factoryInterfaceGenerator{
				GoGenerator: generator.GoGenerator{
					OutputFilename: "factory_interfaces.go",
				},
				outputPackage:                          outputPkg,
				imports:                                imports.NewImportTrackerForPackage(outputPkg),
				clientSetPackage:                       args.VersionedClientSetPackage,
				singleClusterVersionedClientSetPackage: args.SingleClusterVersionedClientSetPackage,
				singleClusterInformersPackage:          args.SingleClusterInformersPackage,
			})

			return generators
		},
	}
}

func groupTarget(outputDirBase, outputPackageBase string, groupVersions clientgentypes.GroupVersions, boilerplate []byte, args *args.Args) generator.Target {
	outputDir := filepath.Join(outputDirBase, groupVersions.PackageName)
	outputPkg := path.Join(outputPackageBase, groupVersions.PackageName)
	groupPkgName := strings.Split(groupVersions.PackageName, ".")[0]

	return &generator.SimpleTarget{
		PkgName:       groupPkgName,
		PkgPath:       outputPkg,
		PkgDir:        outputDir,
		HeaderComment: boilerplate,
		GeneratorsFunc: func(_ *generator.Context) (generators []generator.Generator) {
			generators = append(generators, &groupInterfaceGenerator{
				GoGenerator: generator.GoGenerator{
					OutputFilename: "interface.go",
				},
				outputPackage:                 outputPkg,
				groupVersions:                 groupVersions,
				imports:                       imports.NewImportTrackerForPackage(outputPkg),
				internalInterfacesPackage:     path.Join(outputPackageBase, subdirForInternalInterfaces),
				singleClusterInformersPackage: args.SingleClusterInformersPackage,
			})
			return generators
		},
		FilterFunc: func(c *generator.Context, t *types.Type) bool {
			tags := util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
			return tags.GenerateClient && tags.HasVerb("list") && tags.HasVerb("watch")
		},
	}
}

func versionTarget(
	outputDirBase, outputPkgBase string, groupPkgName string, gv clientgentypes.GroupVersion, groupGoName string,
	boilerplate []byte, typesToGenerate []*types.Type, clientSetPackage string, args *args.Args,
) generator.Target {
	subdir := []string{groupPkgName, strings.ToLower(gv.Version.NonEmpty())}
	outputDir := filepath.Join(outputDirBase, filepath.Join(subdir...))
	outputPkg := path.Join(outputPkgBase, path.Join(subdir...))

	return &generator.SimpleTarget{
		PkgName:       strings.ToLower(gv.Version.NonEmpty()),
		PkgPath:       outputPkg,
		PkgDir:        outputDir,
		HeaderComment: boilerplate,
		GeneratorsFunc: func(_ *generator.Context) (generators []generator.Generator) {
			generators = append(generators, &versionInterfaceGenerator{
				GoGenerator: generator.GoGenerator{
					OutputFilename: "interface.go",
				},
				outputPackage:                 outputPkg,
				imports:                       imports.NewImportTrackerForPackage(outputPkg),
				types:                         typesToGenerate,
				internalInterfacesPackage:     path.Join(outputPkgBase, subdirForInternalInterfaces),
				singleClusterInformersPackage: args.SingleClusterInformersPackage,
			})

			for _, t := range typesToGenerate {
				generators = append(generators, &informerGenerator{
					GoGenerator: generator.GoGenerator{
						OutputFilename: strings.ToLower(t.Name.Name) + ".go",
					},
					outputPackage:                          outputPkg,
					groupPkgName:                           groupPkgName,
					groupVersion:                           gv,
					groupGoName:                            groupGoName,
					typeToGenerate:                         t,
					imports:                                imports.NewImportTrackerForPackage(outputPkg),
					clientSetPackage:                       clientSetPackage,
					listersPackage:                         args.ListersPackage,
					internalInterfacesPackage:              path.Join(outputPkgBase, subdirForInternalInterfaces),
					singleClusterListersPackage:            args.SingleClusterListersPackage,
					singleClusterInformersPackage:          args.SingleClusterInformersPackage,
					singleClusterVersionedClientSetPackage: args.SingleClusterVersionedClientSetPackage,
				})
			}
			return generators
		},
		FilterFunc: func(c *generator.Context, t *types.Type) bool {
			tags := util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
			return tags.GenerateClient && tags.HasVerb("list") && tags.HasVerb("watch")
		},
	}
}
