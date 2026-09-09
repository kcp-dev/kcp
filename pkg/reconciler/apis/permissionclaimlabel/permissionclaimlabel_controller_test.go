/*
Copyright 2026 The kcp Authors.

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

package permissionclaimlabel

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

// reconcileError mirrors how reconcile wraps the per-claim errors it returns:
// the first few are aggregated and wrapped once more with context.
func reconcileError(claimErrs ...error) error {
	err := fmt.Errorf("%d error(s) applying permission claims for APIBinding root|b (showing the first %d): %w",
		len(claimErrs), len(claimErrs), utilerrors.NewAggregate(claimErrs))
	if onlyInformerNotReady(claimErrs) {
		return &informerNotReadyError{err: err}
	}
	return err
}

func TestWaitingForInformer(t *testing.T) {
	t.Parallel()

	missing := fmt.Errorf("%w for wildwest.dev.cowboys", errInformerNotReady)
	listFailed := errors.New("error listing group=\"wildwest.dev\", resource=\"cowboys\": boom")
	commitFailed := errors.New("failed to patch APIBinding: conflict")

	tests := map[string]struct {
		err  error
		want bool
	}{
		"nil":             {err: nil, want: false},
		"plain error":     {err: listFailed, want: false},
		"empty aggregate": {err: utilerrors.NewAggregate(nil), want: false},
		"only informer missing, as process returns it": {
			err:  utilerrors.NewAggregate([]error{reconcileError(missing)}),
			want: true,
		},
		"several claims all waiting": {
			err:  utilerrors.NewAggregate([]error{reconcileError(missing, fmt.Errorf("%w for a.b", errInformerNotReady))}),
			want: true,
		},
		"waiting mixed with a real claim failure": {
			err:  utilerrors.NewAggregate([]error{reconcileError(missing, listFailed)}),
			want: false,
		},
		"waiting but the status commit failed too": {
			err:  utilerrors.NewAggregate([]error{reconcileError(missing), commitFailed}),
			want: false,
		},
		"only the commit failed": {
			err:  utilerrors.NewAggregate([]error{commitFailed}),
			want: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, waitingForInformer(tc.err))
		})
	}
}

func TestOnlyInformerNotReady(t *testing.T) {
	t.Parallel()

	missing := fmt.Errorf("%w for wildwest.dev.cowboys", errInformerNotReady)
	require.False(t, onlyInformerNotReady(nil))
	require.True(t, onlyInformerNotReady([]error{missing}))
	require.True(t, onlyInformerNotReady([]error{missing, missing}))
	require.False(t, onlyInformerNotReady([]error{missing, errors.New("other")}))
}
