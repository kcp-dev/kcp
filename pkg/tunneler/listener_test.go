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

// Based on https://github.com/golang/build/blob/master/revdial/v2/revdial.go
package tunneler

import "testing"

func Test_SyncerTunnelURL(t *testing.T) {

	tests := []struct {
		name    string
		host    string
		ws      string
		target  string
		want    string
		wantErr bool
	}{
		{
			name:   "valid",
			host:   "https://host:9443/base",
			ws:     "myws",
			target: "syncer001",
			want:   "https://host:9443/base/services/syncer-tunnels/clusters/myws/apis/workload.kcp.dev/v1alpha1/synctargets/syncer001",
		},
		{
			name:    "invalid host scheme",
			host:    "http://host:9443/base",
			ws:      "myws",
			target:  "syncer001",
			wantErr: true,
		},
		{
			name:    "invalid host port",
			host:    "https://host:port/base",
			ws:      "myws",
			target:  "syncer001",
			wantErr: true,
		},
		{
			name:    "empty ws",
			host:    "https://host:9443/base",
			ws:      "",
			target:  "syncer002",
			wantErr: true,
		},

		{
			name:    "empty target",
			host:    "https://host:9443/base",
			ws:      "myws",
			target:  "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SyncerTunnelURL(tt.host, tt.ws, tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("SyncerTunnelURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("SyncerTunnelURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
