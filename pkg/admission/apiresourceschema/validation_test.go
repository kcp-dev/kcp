package apiresourceschema

import (
	"reflect"
	"testing"
)

func TestValidationOptionDrift(t *testing.T) {
	expectedNonBool := map[string]reflect.Kind{
		"DisallowDefaultsReason": reflect.String,
	}
	expectedFalse := map[string]bool{
		"RequireImmutableNames": true,
	}

	v := reflect.ValueOf(defaultValidationOpts)
	for _, f := range reflect.VisibleFields(reflect.TypeOf(defaultValidationOpts)) {
		if f.Type.Kind() != reflect.Bool {
			if expected, found := expectedNonBool[f.Name]; found {
				if f.Type.Kind() != expected {
					t.Errorf("expected %s to be %s, got %s", f.Name, expected, f.Type.Kind())
				}
			} else {
				t.Errorf("unexpected field %s of type %s", f.Name, f.Type.Kind())
			}
			continue
		} else if fieldValue := v.FieldByIndex(f.Index).Bool(); !fieldValue && !expectedFalse[f.Name] {
			t.Errorf("unexpected value `true` for field %s, got `false`, probably newly added", f.Name)
		}
	}
}
