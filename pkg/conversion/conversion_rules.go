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

package conversion

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker/decls"
	expr "google.golang.org/genproto/googleapis/api/expr/v1alpha1"

	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema/cel/library"
	"k8s.io/apiextensions-apiserver/third_party/forked/celopenapi/model"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

const celCheckFrequency = 100

// CompiledRule contains the compiled cel.Program to convert a single field.
type CompiledRule struct {
	// FromPath is an expression specifying the field in the source version, such as spec.firstName.
	FromPath string
	// FromFields is FromPath split by '.', with any leading '.' removed.
	FromFields []string
	// ToPath is an expression specifying the destination field in the target version, such as spec.name.first.
	ToPath string
	// ToFields is ToPath split by '.', with any leading '.' removed.
	ToFields []string
	// Program is a compiled set of instructions for a conversion transformation.
	Program cel.Program
}

// Compile compiles conversion rules.
func Compile(
	apiConversion *apisv1alpha1.APIConversion,
	structuralSchemas map[string]*structuralschema.Structural,
) (map[string][]*CompiledRule, error) {
	compiledRules := make(map[string][]*CompiledRule)
	var errs []error

	for i := range apiConversion.Spec.Conversions {
		c := apiConversion.Spec.Conversions[i]
		path := field.NewPath("spec", "conversions").Index(i)
		rulesForVersion, err := compileConversion(path, &c, structuralSchemas)
		if err != nil {
			errs = append(errs, err)
			continue
		}

		compiledRules[c.From] = rulesForVersion
	}

	return compiledRules, kerrors.NewAggregate(errs)
}

// compileConversion processes an APIVersionConversion, compiles any CEL transformations, and returns a slice of
// CompiledRule objects that the converter uses to perform conversions.
func compileConversion(path *field.Path, c *apisv1alpha1.APIVersionConversion, structuralSchemas map[string]*structuralschema.Structural) ([]*CompiledRule, error) {
	rulesForVersion := make([]*CompiledRule, 0, len(c.Rules))

	schema, exists := structuralSchemas[c.From]
	if !exists {
		return nil, field.Invalid(path.Child("from"), c.From, "unable to find structural schema for version")
	}

	var errs []error
	for j, rule := range c.Rules {
		rulePath := path.Child("rules").Index(j)
		cr, err := compileRule(rulePath, rule, schema)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		rulesForVersion = append(rulesForVersion, cr)
	}

	return rulesForVersion, kerrors.NewAggregate(errs)
}

// compileRule takes a single APIConversionRule, compiles the CEL transformation (if any), and converts the rule to a
// CompiledRule that the converter uses to perform conversions.
func compileRule(rulePath *field.Path, rule apisv1alpha1.APIConversionRule, schema *structuralschema.Structural) (*CompiledRule, error) {
	// if rule.Field starts with a ".", such as .spec.fieldName, strip the leading "."
	fromPath := rule.Field
	if fromPath[0] == '.' {
		fromPath = fromPath[1:]
	}

	// if rule.Destination starts with a ".", such as .spec.fieldName, strip the leading "."
	toPath := rule.Destination
	if toPath[0] == '.' {
		toPath = toPath[1:]
	}

	cr := &CompiledRule{
		FromPath:   fromPath,
		FromFields: strings.Split(fromPath, "."),
		ToPath:     rule.Destination,
		ToFields:   strings.Split(toPath, "."),
	}

	schemaForField, err := getStructuralSchemaForField(schema, cr.FromFields...)
	if err != nil {
		return nil, field.Invalid(rulePath.Child("field"), rule.Field, err.Error())
	}

	if rule.Transformation == "" {
		return cr, nil
	}

	env, err := createCELEnv(schemaForField)
	if err != nil {
		return nil, field.InternalError(rulePath.Child("field"), fmt.Errorf("error creating CEL environment: %w", err))
	}

	ast, issues := env.Compile(rule.Transformation)
	if issues != nil {
		return nil, field.Invalid(rulePath.Child("transformation"), rule.Transformation, fmt.Sprintf("error compiling CEL program: %v", issues.Err()))
	}

	program, err := env.Program(ast,
		cel.EvalOptions(cel.OptOptimize),
		cel.OptimizeRegex(library.ExtensionLibRegexOptimizations...),
		cel.InterruptCheckFrequency(celCheckFrequency),
	)
	if err != nil {
		return nil, field.Invalid(rulePath.Child("transformation"), rule.Transformation, fmt.Sprintf("error creating CEL program: %v", err))
	}

	cr.Program = program

	return cr, nil
}

func createCELEnv(structuralSchema *structuralschema.Structural) (*cel.Env, error) {
	env, err := cel.NewEnv(cel.HomogeneousAggregateLiterals())
	if err != nil {
		return nil, fmt.Errorf("error creating CEL environment: %w", err)
	}

	registry := model.NewRegistry(env)

	// inline local copy of upstream's generateUniqueSelfTypeName()
	scopedTypeName := fmt.Sprintf("selfType%d", time.Now().Nanosecond())

	ruleTypes, err := model.NewRuleTypes(scopedTypeName, structuralSchema, true, registry)
	if err != nil {
		return nil, fmt.Errorf("error creating rule types: %w", err)
	}
	if ruleTypes == nil {
		return nil, fmt.Errorf("unexpected nil rule types")
	}

	opts, err := ruleTypes.EnvOptions(env.TypeProvider())
	if err != nil {
		return nil, fmt.Errorf("error getting CEL environment options: %w", err)
	}

	root, ok := ruleTypes.FindDeclType(scopedTypeName)
	if !ok {
		rootDecl := model.SchemaDeclType(structuralSchema, true)
		if rootDecl == nil {
			return nil, fmt.Errorf("unable to find CEL decl type for %s", structuralSchema.Type)
		}
		root = rootDecl.MaybeAssignTypeName(scopedTypeName)
	}

	var propDecls []*expr.Decl
	propDecls = append(propDecls, decls.NewVar("self", root.ExprType()))

	opts = append(opts, cel.Declarations(propDecls...), cel.HomogeneousAggregateLiterals())
	opts = append(opts, library.ExtensionLibs...)
	return env.Extend(opts...)
}

func getStructuralSchemaForField(s *structuralschema.Structural, fields ...string) (*structuralschema.Structural, error) {
	// Cursor keeps track of the current schema subtree as we navigate down through each field segment. We start at the
	// root of the object (which has apiVersion, metadata, spec, status, etc.).
	cursor := s

	// Keep track of which fields we've already visited so we can be specific in our errors
	visited := make([]string, 0, len(fields))

	// Starting with the initial field (e.g. "spec"), try to resolve the next segment (e.g. "name") until we get
	// to the desired field (e.g. "first").
	for _, f := range fields {
		// Verify that each intermediate field is an object.
		if cursor.Type != "object" {
			return nil, fmt.Errorf("expected field %q to be an object", strings.Join(visited, "."))
		}

		visited = append(visited, f)

		// First, check properties
		if property, exists := cursor.Properties[f]; exists {
			// If we found the field, update cursor to point at its schema, and continue to the next field segment
			cursor = &property
			continue
		}

		// Second, check additional properties
		if cursor.AdditionalProperties != nil && cursor.AdditionalProperties.Structural != nil {
			if property, exists := cursor.AdditionalProperties.Structural.Properties[f]; exists {
				// If we found the field, update cursor to point at its schema, and continue to the next field segment
				cursor = &property
				continue
			}
		}

		// The field didn't exist in either properties or additional properties
		return nil, fmt.Errorf("field %q doesn't exist", strings.Join(visited, "."))
	}

	// Cursor is now set to the schema representing the desired field.
	return cursor, nil
}
