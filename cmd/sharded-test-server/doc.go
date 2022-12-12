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

// sharded-test-server starts the kcp-front-proxy and one or more (in the future) kcp shard.
// It also sets up the required certificates and keys for the front-proxy to connect to the shards:
//
//	     ┌────────────────┐ ┌────────────────┐ ┌────────────────┐
//	     │                │ │                │ │                │ All accept .kcp/client-ca.crt client certs.
//	     │ Root shard     │ │ Shard 1        │ │ Shard 2        │ All accept .kcp/requestheader.crt requests with user/group headers.
//	     │                │ │                │ │                │ All use .kcp/serving-ca.crt compatible serving certs.
//	     │ .kcp-0/kcp.log │ │ .kcp-1/kcp.log │ │ .kcp-2/kcp.log │
//	     │                │ │                │ │                │
//	     └────────────────┘ └────────────────┘ └────────────────┘
//	            ▲ ▲                   ▲               ▲
//	    Watches │ │ Redirects traffic │  ┌────────────┘
//	    shards  │ │ to correct shard  │  │
//	            │ └─────────┐         │  │
//	            │           │.kcp-front-proxy/requestheader.crt/key
//	            │        ┌──┴──────────────┐
//	            └────────┤                 │
//	.kcp/root.kubeconfig │                 │
//	                     │ kcp-front-proxy │
//	                     │                 │
//	                     │                 │
//	                     └─────────────────┘
//	                              ▲
//	        .kcp/admin.kubeconfig │
//	                              │
//	                     e2e test or kubectl
//
// Invocation: cmd/sharded-test-server --v=3 --proxy-v=4 --shard-v=5
//
// It will create generic files in .kcp, proxy files in .kcp-front-proxy and shard specific files in .kcp-0, .kcp-1, .kcp-2.
// The usual .kcp/admin.kubeconfig will direct to the front-proxy. The individual shard .kcp/admin.kubeconfig will direct to
// the shards.
package main
