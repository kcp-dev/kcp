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

// Package proxy provides a reverse proxy that accepts client certificates and
// forwards Common Name and Organizations to backend API servers in HTTP
// headers. The proxy terminates client TLS and communicates with API servers
// via mTLS. Traffic is routed based on paths.
//
// An example configuration:
//
//   - path: /services/
//     backend: https://localhost:6444
//     backend_server_ca: certs/kcp-ca-cert.pem
//     proxy_client_cert: certs/proxy-client-cert.pem
//     proxy_client_key: certs/proxy-client-key.pem
//   - path: /
//     backend: https://localhost:6443
//     backend_server_ca: certs/kcp-ca-cert.pem
//     proxy_client_cert: certs/proxy-client-cert.pem
//     proxy_client_key: certs/proxy-client-key.pem
package proxy
