package conflict

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
)

const schemasMapAnnotation = "conflict.virtualresources.integration.tests.x-kcp.io/synthethic-schemas"

type SchemaDescription struct {
	Group      string
	Version    string
	Kind       string
	Singular   string
	Plural     string
	ListKind   string
	ShortNames []string
}

func (d *SchemaDescription) APIVersion() string {
	return schema.GroupKind{
		Group: d.Group,
		Kind:  d.Kind,
	}.String()
}

func (d *SchemaDescription) GroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    d.Group,
		Version:  d.Version,
		Resource: d.Plural,
	}
}

func setSchemaDescriptionAnnotation(testName string, schDesc *SchemaDescription, apiExport *apisv1alpha2.APIExport) error {
	if apiExport.Annotations == nil {
		apiExport.Annotations = make(map[string]string)
	}

	var m map[string]SchemaDescription
	if ann, found := apiExport.Annotations[schemasMapAnnotation]; found {
		if err := json.Unmarshal([]byte(ann), &m); err != nil {
			return err
		}
	} else {
		m = make(map[string]SchemaDescription)
	}

	m[testName] = *schDesc
	bs, err := json.Marshal(m)
	if err != nil {
		return err
	}

	apiExport.Annotations[schemasMapAnnotation] = string(bs)
	return nil
}

func getSchemaDescriptionFromAnnotation(testName string, apiExport *apisv1alpha2.APIExport) (SchemaDescription, error) {
	if apiExport.Annotations == nil {
		return SchemaDescription{}, fmt.Errorf("annotation %s not set", schemasMapAnnotation)
	}

	ann, found := apiExport.Annotations[schemasMapAnnotation]
	if !found {
		return SchemaDescription{}, fmt.Errorf("annotation %s not set", schemasMapAnnotation)
	}

	var m map[string]SchemaDescription
	if err := json.Unmarshal([]byte(ann), &m); err != nil {
		return SchemaDescription{}, err
	}

	schDesc, found := m[testName]
	if !found {
		return SchemaDescription{}, fmt.Errorf("entry %q not found in %s annotation: has %v", testName, schemasMapAnnotation, m)
	}

	return schDesc, nil
}
