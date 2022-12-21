/*
Copyright 2022 The KCP Authors.

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

package reservedmetadata

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
)

func newAttr(obj, oldObject runtime.Object, op admission.Operation, user user.Info) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		oldObject,
		schema.GroupVersionKind{},
		"",
		"test",
		schema.GroupVersionResource{},
		"",
		op,
		&metav1.CreateOptions{},
		false,
		user,
	)
}

func TestAdmission(t *testing.T) {
	for _, tc := range []struct {
		testName string
		attr     admission.Attributes
		wantErr  string
	}{
		{
			testName: "empty object",
			attr:     newAttr(nil, nil, admission.Create, &user.DefaultInfo{}),
		},
		{
			testName: "empty old object",
			attr:     newAttr(&v1.Pod{}, nil, admission.Create, &user.DefaultInfo{}),
		},
		{
			testName: "unchanged empty labels/annotations",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "unchanged labels/annotations",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "changed label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "added kcp.io label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved label: \"some.kcp.io\"",
		},
		{
			testName: "added empty kcp.io label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved label: \"some.kcp.io\"",
		},
		{
			testName: "deleted kcp.io label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":         "bar",
							"some.kcp.io": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved label: \"some.kcp.io\"",
		},
		{
			testName: "deleted empty kcp.io label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":         "bar",
							"some.kcp.io": "",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved label: \"some.kcp.io\"",
		},
		{
			testName: "created kcp.io label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "new",
							"some.kcp.io": "new",
						},
					},
				},
				nil,
				admission.Create,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved label: \"some.kcp.io\"",
		},
		{
			testName: "created kcp.io label as privileged system user",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "new",
							"some.kcp.io": "new",
						},
					},
				},
				nil,
				admission.Create,
				&user.DefaultInfo{Groups: []string{user.SystemPrivilegedGroup}},
			),
		},
		{
			testName: "changed kcp.io label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":         "bar",
							"some.kcp.io": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved label: \"some.kcp.io\"",
		},
		{
			testName: "changed label preserving kcp.io labels",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":         "bar",
							"some.kcp.io": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "added kcp.io label as privileged system user",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{
					Groups: []string{user.SystemPrivilegedGroup},
				},
			),
		},
		{
			testName: "added allow-listed label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":                "changed",
							"foo.kcp.io/allowed": "added",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "deleted allow-listed label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":                "bar",
							"foo.kcp.io/allowed": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "changed allow-listed label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":                "changed",
							"foo.kcp.io/allowed": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":                "bar",
							"foo.kcp.io/allowed": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "added allow-listed wildcard label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":                "changed",
							"foo.kcp.io/allowed": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "deleted allow-listed wildcard label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":                "bar",
							"foo.kcp.io/allowed": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "changed allow-listed wildcard label",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":                "changed",
							"foo.kcp.io/allowed": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo":                "bar",
							"foo.kcp.io/allowed": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "changed annotations",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Annotations: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Annotations: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
		},
		{
			testName: "added kcp.io annotation",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Annotations: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Annotations: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved annotation: \"some.kcp.io\"",
		},
		{
			testName: "deleted kcp.io annotation",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Annotations: map[string]string{
							"foo": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Annotations: map[string]string{
							"foo":         "bar",
							"some.kcp.io": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved annotation: \"some.kcp.io\"",
		},
		{
			testName: "changed kcp.io annotation",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Annotations: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "changed",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Annotations: map[string]string{
							"foo":         "bar",
							"some.kcp.io": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{},
			),
			wantErr: "forbidden: modification of reserved annotation: \"some.kcp.io\"",
		},
		{
			testName: "added kcp.io annotation as privileged system user",
			attr: newAttr(
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
						Labels: map[string]string{
							"foo":         "changed",
							"some.kcp.io": "bar",
						},
					},
				},
				&v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "bar",
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
				admission.Update,
				&user.DefaultInfo{
					Groups: []string{user.SystemPrivilegedGroup},
				},
			),
		},
	} {
		t.Run(tc.testName, func(t *testing.T) {
			plugin := &reservedMetadata{
				Handler:             admission.NewHandler(admission.Create, admission.Update),
				annotationAllowList: []string{"foo.kcp.io/allowed"},
				labelAllowList:      []string{"foo.kcp.io/allowed"},
			}
			var ctx context.Context

			gotErr := ""
			err := plugin.Validate(ctx, tc.attr, nil)
			if err != nil {
				gotErr = err.Error()
			}

			if gotErr != tc.wantErr {
				t.Errorf("want error %q, got %q", tc.wantErr, gotErr)
			}
		})
	}
}
