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

package schemacompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"go.uber.org/multierr"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// EnsureStructuralSchemaCompatibility compares a new structural schema to an existing one, to ensure that the existing
// schema is a sub-schema of the new schema. In other words that means that all the documents validated by the existing schema
// will also be validated by the new schema, so that the new schema can be considered backward-compatible with the existing schema.
// If it's not the case, errors are reported for each incompatible schema change.
//
// PLEASE NOTE that the implementation is incomplete (it's ongoing work), but still consistent:
// if some Json Schema elements are changed and the comparison of this type of element is not yet implemented,
// then an incompatible change error is triggered explaining that the comparison on this element type is not supported.
// So there should never be any case when a schema is considered backward-compatible while in fact it is not.
//
// If the narrowExisting argument is true, then the LCD (Lowest-Common-Denominator) between existing schema and the new schema
// is built (when possible), and returned if no incompatible change was detected (like type change, etc...).
// If the narrowExisting argument is false, the existing schema is untouched and no LCD schema is calculated.
//
// In either case, when no errors are reported, it is ensured that either the existing schema or the calculated LCD
// is a sub-schema of the new schema.
func EnsureStructuralSchemaCompatibility(fldPath *field.Path, existing, new *apiextensionsv1.JSONSchemaProps, narrowExisting bool) (*apiextensionsv1.JSONSchemaProps, error) {
	var newInternal, existingInternal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(existing, &existingInternal, nil); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(new, &newInternal, nil); err != nil {
		return nil, err
	}
	newStrucural, err := schema.NewStructural(&newInternal)
	if err != nil {
		return nil, err
	}

	existingStructural, err := schema.NewStructural(&existingInternal)
	if err != nil {
		return nil, err
	}

	lcdStructural := existingStructural.DeepCopy()
	if err := lcdForStructural(fldPath, existingStructural, newStrucural, lcdStructural, narrowExisting); err != nil {
		return nil, err
	}
	serialized, err := json.Marshal(lcdStructural.ToKubeOpenAPI())
	if err != nil {
		return nil, err
	}
	var jsonSchemaProps apiextensionsv1.JSONSchemaProps
	if err := json.Unmarshal(serialized, &jsonSchemaProps); err != nil {
		return nil, err
	}
	return &jsonSchemaProps, nil
}

func checkTypesAreTheSame(fldPath *field.Path, existing, new *schema.Structural) error {
	if new.Type != existing.Type {
		return field.Invalid(fldPath.Child("type"), new.Type, fmt.Sprintf("The type changed (was %q, now %q)", existing.Type, new.Type))
	}
	return nil
}

func checkUnsupportedValidation(fldPath *field.Path, existing, new interface{}, validationName, typeName string) error {
	if !reflect.ValueOf(existing).IsZero() || !reflect.ValueOf(new).IsZero() {
		return field.Forbidden(fldPath, fmt.Sprintf("The %q JSON Schema construct is not supported by the Schema negotiation for type %q", validationName, typeName))
	}
	return nil
}

func floatPointersEqual(p1, p2 *float64) bool {
	if p1 == nil && p2 == nil {
		return true
	}
	if p1 != nil && p2 != nil {
		return *p1 == *p2
	}
	return false
}

func intPointersEqual(p1, p2 *int64) bool {
	if p1 == nil && p2 == nil {
		return true
	}
	if p1 != nil && p2 != nil {
		return *p1 == *p2
	}
	return false
}

func stringPointersEqual(p1, p2 *string) bool {
	if p1 == nil && p2 == nil {
		return true
	}
	if p1 != nil && p2 != nil {
		return *p1 == *p2
	}
	return false
}

func checkUnsupportedValidationForNumerics(fldPath *field.Path, existing, new *schema.ValueValidation, typeName string) error {
	err := multierr.Combine(
		checkUnsupportedValidation(fldPath, existing.Not, new.Not, "not", typeName),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", typeName),
		checkUnsupportedValidation(fldPath, existing.AnyOf, new.AnyOf, "anyOf", typeName),
		checkUnsupportedValidation(fldPath, existing.OneOf, new.OneOf, "oneOf", typeName),
		checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", typeName))
	if !floatPointersEqual(new.Maximum, existing.Maximum) ||
		!floatPointersEqual(new.Minimum, existing.Minimum) ||
		new.ExclusiveMaximum != existing.ExclusiveMaximum ||
		new.ExclusiveMinimum != existing.ExclusiveMinimum {
		err = multierr.Combine(
			err,
			checkUnsupportedValidation(fldPath, existing.Maximum, new.Maximum, "maximum", typeName),
			checkUnsupportedValidation(fldPath, existing.Minimum, new.Minimum, "minimum", typeName))
	}
	if !floatPointersEqual(new.MultipleOf, existing.MultipleOf) {
		multierr.AppendInto(&err, checkUnsupportedValidation(fldPath, existing.MultipleOf, new.MultipleOf, "multipleOf", typeName))
	}
	return err
}

func lcdForStructural(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	if lcd == nil && narrowExisting {
		return field.InternalError(fldPath, errors.New("lcd argument should be passed when narrowExisting is true"))
	}
	if new == nil {
		return field.Invalid(fldPath, nil, "new schema doesn't allow anything")
	}
	if was, now := existing.XPreserveUnknownFields, new.XPreserveUnknownFields; was != now {
		return field.Invalid(fldPath.Child("x-kubernetes-preserve-unknown-fields"), new.XPreserveUnknownFields, fmt.Sprintf("x-kubernetes-preserve-unknown-fields value changed (was %t, now %t)", was, now))
	}

	switch existing.Type {
	case "number":
		return lcdForNumber(fldPath, existing, new, lcd, narrowExisting)
	case "integer":
		return lcdForInteger(fldPath, existing, new, lcd, narrowExisting)
	case "string":
		return lcdForString(fldPath, existing, new, lcd, narrowExisting)
	case "boolean":
		return lcdForBoolean(fldPath, existing, new, lcd, narrowExisting)
	case "array":
		return lcdForArray(fldPath, existing, new, lcd, narrowExisting)
	case "object":
		return lcdForObject(fldPath, existing, new, lcd, narrowExisting)
	case "":
		if existing.XIntOrString {
			return lcdForIntOrString(fldPath, existing, new, lcd, narrowExisting)
		} else if existing.XPreserveUnknownFields {
			return lcdForPreserveUnknownFields(fldPath, existing, new, lcd, narrowExisting)
		}
	}
	return field.Invalid(field.NewPath(fldPath.String(), "type"), existing.Type, "Invalid type")
}

func lcdForIntegerValidation(fldPath *field.Path, existing, new *schema.ValueValidation, lcd *schema.ValueValidation, narrowExisting bool) error {
	return checkUnsupportedValidationForNumerics(fldPath, existing, new, "integer")
}

func lcdForNumberValidation(fldPath *field.Path, existing, new *schema.ValueValidation, lcd *schema.ValueValidation, narrowExisting bool) error {
	return checkUnsupportedValidationForNumerics(fldPath, existing, new, "numbers")
}

func lcdForNumber(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	if new.Type == "integer" {
		// new type is a subset of the existing type.
		if !narrowExisting {
			return checkTypesAreTheSame(fldPath, existing, new)
		}
		lcd.Type = new.Type
		return lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)
	}

	if err := checkTypesAreTheSame(fldPath, existing, new); err != nil {
		return err
	}

	return lcdForNumberValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)
}

func lcdForInteger(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	if new.Type == "number" {
		// new type is a superset of the existing type.
		// all is well type-wise
		// keep the existing type (integer) in the LCD
	} else {
		if err := checkTypesAreTheSame(fldPath, existing, new); err != nil {
			return err
		}
	}
	return lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting)
}

func lcdForStringValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) error {
	err := multierr.Combine(
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "string"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "string"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "string"))
	if !intPointersEqual(new.MaxLength, existing.MaxLength) ||
		!intPointersEqual(new.MinLength, existing.MinLength) {
		err = multierr.Combine(
			err,
			checkUnsupportedValidation(fldPath, existing.MaxLength, new.MaxLength, "maxLength", "string"),
			checkUnsupportedValidation(fldPath, existing.MinLength, new.MinLength, "minLength", "string"))
	}
	if new.Pattern != existing.Pattern {
		multierr.AppendInto(&err, checkUnsupportedValidation(fldPath, existing.Pattern, new.Pattern, "pattern", "string"))
	}
	toEnumSets := func(enum []schema.JSON) sets.Set[string] {
		enumSet := sets.New[string]()
		for _, val := range enum {
			strVal, isString := val.Object.(string)
			if !isString {
				multierr.AppendInto(&err, field.Invalid(fldPath.Child("enum"), enum, "enum value should be a 'string' for Json type 'string'"))
				continue
			}
			enumSet.Insert(strVal)
		}
		return enumSet
	}
	existingEnumValues := toEnumSets(existing.Enum)
	newEnumValues := toEnumSets(new.Enum)
	if !newEnumValues.IsSuperset(existingEnumValues) {
		if !narrowExisting {
			multierr.AppendInto(&err, field.Invalid(fldPath.Child("enum"), sets.List[string](newEnumValues.Difference(existingEnumValues)), "enum value has been changed in an incompatible way"))
		}
		lcd.Enum = nil
		lcdEnumValues := sets.List[string](existingEnumValues.Intersection(newEnumValues))
		for _, val := range lcdEnumValues {
			lcd.Enum = append(lcd.Enum, schema.JSON{Object: val})
		}
	}

	if existing.Format != new.Format {
		multierr.AppendInto(&err, field.Invalid(fldPath.Child("format"), new.Format, "format value has been changed in an incompatible way"))
	}
	return err
}

func lcdForString(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	return multierr.Combine(
		checkTypesAreTheSame(fldPath, existing, new),
		lcdForStringValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting))
}

func lcdForBooleanValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) error {
	return multierr.Combine(
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "boolean"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "boolean"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "boolean"),
		checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", "boolean"))
}

func lcdForBoolean(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	return multierr.Combine(
		checkTypesAreTheSame(fldPath, existing, new),
		lcdForBooleanValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting))
}

func lcdForArrayValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) error {
	err := multierr.Combine(
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "array"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anytOf", "array"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "array"),
		checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", "array"))
	if !intPointersEqual(new.MaxItems, existing.MaxItems) ||
		!intPointersEqual(new.MinItems, existing.MinItems) {
		err = multierr.Combine(
			err,
			checkUnsupportedValidation(fldPath, existing.MaxLength, new.MaxLength, "maxItems", "array"),
			checkUnsupportedValidation(fldPath, existing.MinLength, new.MinLength, "minItems", "array"))
	}
	if !existing.UniqueItems && new.UniqueItems {
		if !narrowExisting {
			multierr.AppendInto(&err, field.Invalid(fldPath.Child("uniqueItems"), new.UniqueItems, "uniqueItems value has been changed in an incompatible way"))
		} else {
			lcd.UniqueItems = true
		}
	}
	return err
}

func lcdForArray(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	err := multierr.Combine(
		checkTypesAreTheSame(fldPath, existing, new),
		lcdForArrayValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting),
		lcdForStructural(fldPath.Child("Items"), existing.Items, new.Items, lcd.Items, narrowExisting))
	if !stringPointersEqual(existing.Extensions.XListType, new.Extensions.XListType) {
		multierr.AppendInto(&err, field.Invalid(fldPath.Child("x-kubernetes-list-type"), new.Extensions.XListType, "x-kubernetes-list-type value has been changed in an incompatible way"))
	}
	if !sets.New[string](existing.Extensions.XListMapKeys...).Equal(sets.New[string](new.Extensions.XListMapKeys...)) {
		multierr.AppendInto(&err, field.Invalid(fldPath.Child("x-kubernetes-list-map-keys"), new.Extensions.XListType, "x-kubernetes-list-map-keys value has been changed in an incompatible way"))
	}
	return err
}

func lcdForObjectValidation(fldPath *field.Path, existing, new, lcd *schema.ValueValidation, narrowExisting bool) error {
	return multierr.Combine(
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "allOf", "object"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "anyOf", "object"),
		checkUnsupportedValidation(fldPath, existing.AllOf, new.AllOf, "oneOf", "object"),
		checkUnsupportedValidation(fldPath, existing.Enum, new.Enum, "enum", "object"))
}

func lcdForObject(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	err := checkTypesAreTheSame(fldPath, existing, new)

	if !stringPointersEqual(existing.Extensions.XMapType, new.Extensions.XMapType) {
		multierr.AppendInto(&err, field.Invalid(fldPath.Child("x-kubernetes-map-type"), new.Extensions.XListType, "x-kubernetes-map-type value has been changed in an incompatible way"))
	}

	// Let's keep in mind that, in structural schemas, properties and additionalProperties are mutually exclusive,
	// which greatly simplifies the logic here.

	if len(existing.Properties) > 0 {
		if len(new.Properties) > 0 {
			existingProperties := sets.StringKeySet(existing.Properties)
			newProperties := sets.StringKeySet(new.Properties)
			lcdProperties := existingProperties
			if !newProperties.IsSuperset(existingProperties) {
				if !narrowExisting {
					multierr.AppendInto(&err, field.Invalid(fldPath.Child("properties"), existingProperties.Difference(newProperties).List(), "properties have been removed in an incompatible way"))
				}
				lcdProperties = existingProperties.Intersection(newProperties)
			}
			for _, key := range lcdProperties.List() {
				existingPropertySchema := existing.Properties[key]
				newPropertySchema := new.Properties[key]
				lcdPropertySchema := lcd.Properties[key]
				multierr.AppendInto(&err, lcdForStructural(fldPath.Child("properties").Key(key), &existingPropertySchema, &newPropertySchema, &lcdPropertySchema, narrowExisting))
				lcd.Properties[key] = lcdPropertySchema
			}
			for _, removedProperty := range existingProperties.Difference(lcdProperties).UnsortedList() {
				delete(lcd.Properties, removedProperty)
			}
		} else if new.AdditionalProperties != nil && new.AdditionalProperties.Structural != nil {
			for _, key := range sets.StringKeySet(existing.Properties).List() {
				existingPropertySchema := existing.Properties[key]
				lcdPropertySchema := lcd.Properties[key]
				multierr.AppendInto(&err, lcdForStructural(fldPath.Child("properties").Key(key), &existingPropertySchema, new.AdditionalProperties.Structural, &lcdPropertySchema, narrowExisting))
				lcd.Properties[key] = lcdPropertySchema
			}
		} else if new.AdditionalProperties != nil && new.AdditionalProperties.Bool {
			// that allows named properties only.
			// => Keep the existing schemas as the lcd.
		} else {
			multierr.AppendInto(&err, field.Invalid(fldPath.Child("properties"), sets.StringKeySet(existing.Properties).List(), "properties value has been completely cleared in an incompatible way"))
		}
	} else if existing.AdditionalProperties != nil {
		if existing.AdditionalProperties.Structural != nil {
			if new.AdditionalProperties.Structural != nil {
				multierr.AppendInto(&err, lcdForStructural(fldPath.Child("additionalProperties"), existing.AdditionalProperties.Structural, new.AdditionalProperties.Structural, lcd.AdditionalProperties.Structural, narrowExisting))
			} else if existing.AdditionalProperties != nil && new.AdditionalProperties.Bool {
				// new schema allows any properties of any schema here => it is a superset of the existing schema
				// that allows any properties of a given schema.
				// => Keep the existing schemas as the lcd.
			} else {
				multierr.AppendInto(&err, field.Invalid(fldPath.Child("additionalProperties"), new.AdditionalProperties.Bool, "additionalProperties value has been changed in an incompatible way"))
			}
		} else if existing.AdditionalProperties.Bool {
			if !new.AdditionalProperties.Bool {
				if !narrowExisting {
					multierr.AppendInto(&err, field.Invalid(fldPath.Child("additionalProperties"), new.AdditionalProperties.Bool, "additionalProperties value has been changed in an incompatible way"))
				}
				lcd.AdditionalProperties.Bool = false
				lcd.AdditionalProperties.Structural = new.AdditionalProperties.Structural
			}
		}
	}

	multierr.AppendInto(&err, lcdForObjectValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting))

	return err
}

func lcdForIntOrString(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	err := checkTypesAreTheSame(fldPath, existing, new)
	if !new.XIntOrString {
		multierr.AppendInto(&err, field.Invalid(fldPath.Child("x-kubernetes-int-or-string"), new.XIntOrString, "x-kubernetes-int-or-string value has been changed in an incompatible way"))
	}

	// We special-case IntOrString, since they are expected to have a fixed AnyOf value.
	// So we'll check the AnyOf separately and remove it from the further string-related or int-related validation
	// where anyOf is currently not supported.
	existingAnyOf := existing.ValueValidation.AnyOf
	newAnyOf := new.ValueValidation.AnyOf
	if !reflect.DeepEqual(existingAnyOf, newAnyOf) {
		multierr.AppendInto(&err, field.Invalid(fldPath.Child("anyOf"), newAnyOf, "anyOf value has been changed in an incompatible way"))
	}
	existing.ValueValidation.AnyOf = nil
	new.ValueValidation.AnyOf = nil
	err = multierr.Combine(
		err,
		lcdForStringValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting),
		lcdForIntegerValidation(fldPath, existing.ValueValidation, new.ValueValidation, lcd.ValueValidation, narrowExisting))
	existing.ValueValidation.AnyOf = existingAnyOf
	lcd.ValueValidation.AnyOf = existingAnyOf
	new.ValueValidation.AnyOf = newAnyOf

	return err
}

func lcdForPreserveUnknownFields(fldPath *field.Path, existing, new *schema.Structural, lcd *schema.Structural, narrowExisting bool) error {
	return checkTypesAreTheSame(fldPath, existing, new)
}
