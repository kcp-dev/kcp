package schemas

import (
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	rootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
)

var TenancyKcpDevSchemas = map[string]*apisv1alpha1.APIResourceSchema{}

func init() {
	workspaceResource := apisv1alpha1.APIResourceSchema{}
	if err := rootphase0.Unmarshal("apiresourceschema-workspaces.tenancy.kcp.dev.yaml", &workspaceResource); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workspace resource: %w", err)
	}
	bs, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
		Type:                   "object",
		XPreserveUnknownFields: pointer.BoolPtr(true),
	})
	if err != nil {
		return nil, err
	}
	for i := range workspaceResource.Spec.Versions {
		v := &workspaceResource.Spec.Versions[i]
		v.Schema.Raw = bs // wipe schemas. We don't want validation here.
	}
}
