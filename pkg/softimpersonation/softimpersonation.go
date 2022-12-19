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

package softimpersonation

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
)

const (
	softImpersonationHeader = "X-Kcp-Internal-Soft-Impersonation"
)

// WithSoftImpersonatedConfig returns a clone of the input rest.Config
// with an additional header containing the given user info marshalled in Json.
func WithSoftImpersonatedConfig(config *rest.Config, userInfo kuser.Info) (*rest.Config, error) {
	impersonatedonfig := rest.CopyConfig(config)

	userInfoJson, err := marshalUserInfo(userInfo)
	if err != nil {
		return nil, err
	}

	impersonatedonfig.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		// we have to use this way because the dynamic client overrides the content-type :-/
		return &softImpersonationTransport{
			RoundTripper: rt,
			userInfoJson: userInfoJson,
		}
	})
	return impersonatedonfig, nil
}

// softImpersonationTransport adds the 'X-Kcp-Internal-Soft-Impersonation'
// header to the request.
type softImpersonationTransport struct {
	http.RoundTripper
	userInfoJson string
}

func (t *softImpersonationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(softImpersonationHeader, t.userInfoJson)
	return t.RoundTripper.RoundTrip(req)
}

// marshalUserInfo builds a string that contains
// the user information, marshalled as json.
func marshalUserInfo(userInfo kuser.Info) (string, error) {
	if userInfo == nil {
		return "", errors.New("no user info")
	}
	info := &authenticationv1.UserInfo{
		Username: userInfo.GetName(),
		UID:      userInfo.GetUID(),
		Groups:   userInfo.GetGroups(),
	}
	extra := map[string]authenticationv1.ExtraValue{}
	for k, v := range userInfo.GetExtra() {
		extra[k] = v
	}
	info.Extra = extra
	rawInfo, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("failed to marshal user info: %w", err)
	}

	return string(rawInfo), nil
}

// unmarshalUserInfo builds a user.Info object from a string
// that contains the user information marshalled as json.
func unmarshalUserInfo(userInfoJson string) (kuser.Info, error) {
	info := authenticationv1.UserInfo{}
	if err := json.Unmarshal([]byte(userInfoJson), &info); err != nil {
		return nil, err
	}

	var extra map[string][]string
	if info.Extra != nil {
		extra = make(map[string][]string)
		for k, v := range info.Extra {
			extra[k] = v
		}
	}

	return &kuser.DefaultInfo{
		Name:   info.Username,
		UID:    info.UID,
		Groups: info.Groups,
		Extra:  extra,
	}, nil
}

// UserInfoFromRequestHeader extract a user.Info object from
// an internal request header set by a softly impersonated client.
func UserInfoFromRequestHeader(r *http.Request) (kuser.Info, error) {
	val := r.Header.Get(softImpersonationHeader)
	if val == "" {
		return nil, nil
	}

	user, ok := request.UserFrom(r.Context())
	if !ok {
		return nil, errors.New("permission to do soft impersonation could not be checked")
	}
	if !sets.NewString(user.GetGroups()...).Has(kuser.SystemPrivilegedGroup) {
		return nil, errors.New("soft impersonation not allowed")
	}
	return unmarshalUserInfo(val)
}
