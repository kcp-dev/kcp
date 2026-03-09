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

// Package apiresourceschema provides a virtual workspace that exposes
// APIResourceSchemas for all APIBindings in a given workspace.
// This allows consumers to access schema definitions for APIs they have bound.
//
// # APIResourceSchema Virtual Workspace
//
// This virtual workspace provides read-only access to APIResourceSchemas that are
// referenced by APIBindings in a consumer workspace. It enables consumers and
// authorized users to discover schema definitions for APIs they have bound.
//
//	                    APIResourceSchema Virtual Workspace
//	===========================================================================
//
//	                         VW URL Format
//	---------------------------------------------------------------------------
//	/services/apiresourceschema/<consumer-cluster>/clusters/<wildcard>/apis/apis.kcp.io/v1alpha1/apiresourceschemas
//	          └────────┬──────┘ └───────┬────────┘
//	               VW Name        Consumer Cluster
//	                             (from URL path)
//
//	                         Architecture
//	---------------------------------------------------------------------------
//
//	┌─────────────────────────────────────────────────────────────────────────┐
//	│                        Consumer Workspace                               │
//	│                     (cluster: 1a2b3c4d5e)                               │
//	│                                                                         │
//	│   ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐     │
//	│   │  APIBinding     │    │  APIBinding     │    │  APIBinding     │     │
//	│   │  "widgets.io"   │    │  "gadgets.co"   │    │  "tools.dev"    │     │
//	│   │      │          │    │      │          │    │      │          │     │
//	│   │      ▼          │    │      ▼          │    │      ▼          │     │
//	│   │ ref: provider1  │    │ ref: provider2  │    │ ref: provider3  │     │
//	│   │      /widgets   │    │      /gadgets   │    │      /tools     │     │
//	│   └─────────────────┘    └─────────────────┘    └─────────────────┘     │
//	│                                                                         │
//	└──────────────────────────────┬──────────────────────────────────────────┘
//	                               │
//	        ┌──────────────────────┼──────────────────────┐
//	        │                      │                      │
//	        ▼                      ▼                      ▼
//	┌───────────────┐      ┌───────────────┐      ┌───────────────┐
//	│   Provider1   │      │   Provider2   │      │   Provider3   │
//	│   Workspace   │      │   Workspace   │      │   Workspace   │
//	│               │      │               │      │               │
//	│ ┌───────────┐ │      │ ┌───────────┐ │      │ ┌───────────┐ │
//	│ │ APIExport │ │      │ │ APIExport │ │      │ │ APIExport │ │
//	│ │ "widgets" │ │      │ │ "gadgets" │ │      │ │ "tools"   │ │
//	│ │     │     │ │      │ │     │     │ │      │ │     │     │ │
//	│ │     ▼     │ │      │ │     ▼     │ │      │ │     ▼     │ │
//	│ │ Schema:   │ │      │ │ Schema:   │ │      │ │ Schema:   │ │
//	│ │ widgets.v1│ │      │ │ gadgets.v1│ │      │ │ tools.v1  │ │
//	│ └───────────┘ │      │ └───────────┘ │      │ └───────────┘ │
//	└───────────────┘      └───────────────┘      └───────────────┘
//
//	                   ║
//	                   ║  VW aggregates schemas from
//	                   ║  all bound APIExports
//	                   ▼
//
//	┌─────────────────────────────────────────────────────────────────────────┐
//	│             APIResourceSchema Virtual Workspace                         │
//	│             /services/apiresourceschema/1a2b3c4d5e                      │
//	│                                                                         │
//	│  ┌───────────────────────────────────────────────────────────────────┐  │
//	│  │  LIST /apiresourceschemas                                         │  │
//	│  │  ┌────────────┐  ┌────────────┐  ┌────────────┐                   │  │
//	│  │  │widgets.v1  │  │gadgets.v1  │  │tools.v1    │                   │  │
//	│  │  └────────────┘  └────────────┘  └────────────┘                   │  │
//	│  └───────────────────────────────────────────────────────────────────┘  │
//	│                                                                         │
//	│  Supported Verbs: GET | LIST | WATCH (read-only)                        │
//	└─────────────────────────────────────────────────────────────────────────┘
//
//
//	                         Request Flow
//	---------------------------------------------------------------------------
//
//	  Client Request
//	       │
//	       ▼
//	┌──────────────────────────────────────────────────────────────────────┐
//	│ URL: /services/apiresourceschema/<cluster>/clusters/...             │
//	└──────────────────────────────────────────────────────────────────────┘
//	       │
//	       ▼
//	┌──────────────────────────────────────────────────────────────────────┐
//	│ 1. Parse URL                                                         │
//	│    - Extract consumer cluster name from URL path                     │
//	│    - Strip /clusters/<wildcard>/ segment (compatibility)             │
//	│    - Set APIDomainKey = consumer cluster                             │
//	└──────────────────────────────────────────────────────────────────────┘
//	       │
//	       ▼
//	┌──────────────────────────────────────────────────────────────────────┐
//	│ 2. Authorization Check                                               │
//	│    ┌────────────────────────────────────────────────────────────┐    │
//	│    │  Is verb read-only (get/list/watch)?                       │    │
//	│    │      NO  ──────────► DENY: "only read-only access allowed" │    │
//	│    │      │                                                     │    │
//	│    │      YES                                                   │    │
//	│    │      ▼                                                     │    │
//	│    │  User has "get" on "apibindings" in consumer cluster?      │    │
//	│    │      NO  ──────────► DENY: "needs apibindings permission"  │    │
//	│    │      │                                                     │    │
//	│    │      YES                                                   │    │
//	│    │      ▼                                                     │    │
//	│    │  ALLOW                                                     │    │
//	│    └────────────────────────────────────────────────────────────┘    │
//	└──────────────────────────────────────────────────────────────────────┘
//	       │
//	       ▼
//	┌──────────────────────────────────────────────────────────────────────┐
//	│ 3. Build API Definition Set                                          │
//	│    - List all APIBindings in consumer cluster                        │
//	│    - Filter: status.phase == "Bound"                                 │
//	│    - For each binding:                                               │
//	│        - Get referenced APIExport from provider cluster              │
//	│        - Collect schema names from export.spec.resources             │
//	└──────────────────────────────────────────────────────────────────────┘
//	       │
//	       ▼
//	┌──────────────────────────────────────────────────────────────────────┐
//	│ 4. Serve Request                                                     │
//	│    ┌──────────────────────────────────────────────────────────────┐  │
//	│    │ GET /apiresourceschemas/<name>                               │  │
//	│    │   - Check if schema name is in collected schemas             │  │
//	│    │   - If not found ──────────► NotFound error                  │  │
//	│    │   - Fetch schema from source cluster                         │  │
//	│    │   - Return as unstructured object                            │  │
//	│    └──────────────────────────────────────────────────────────────┘  │
//	│    ┌──────────────────────────────────────────────────────────────┐  │
//	│    │ LIST /apiresourceschemas                                     │  │
//	│    │   - Iterate all collected schemas                            │  │
//	│    │   - Fetch each from source cluster                           │  │
//	│    │   - Return as UnstructuredList                               │  │
//	│    └──────────────────────────────────────────────────────────────┘  │
//	└──────────────────────────────────────────────────────────────────────┘
//
//
//	                         Authorization Model
//	---------------------------------------------------------------------------
//
//	"The Invitation" principle:
//	If you can read APIBindings, you already know what APIs are bound.
//	Therefore, you should be able to see the schemas for those bindings.
//
//	┌─────────────────────────────────────────────────────────────────────────┐
//	│                     Required RBAC Permissions                           │
//	│                                                                         │
//	│  In the CONSUMER workspace, the user must have:                         │
//	│                                                                         │
//	│  apiVersion: rbac.authorization.k8s.io/v1                               │
//	│  kind: ClusterRole                                                      │
//	│  rules:                                                                 │
//	│  - nonResourceURLs: ["/"]        # Workspace access                     │
//	│    verbs: ["access"]             # (for cross-workspace SAs)            │
//	│  - apiGroups: ["apis.kcp.io"]                                           │
//	│    resources: ["apibindings"]                                           │
//	│    verbs: ["get"]                # Minimum required                     │
//	│                                                                         │
//	└─────────────────────────────────────────────────────────────────────────┘
//
//	Cross-Workspace ServiceAccount Access:
//
//	┌─────────────────────────┐           ┌─────────────────────────┐
//	│   Provider Workspace    │           │   Consumer Workspace    │
//	│                         │           │                         │
//	│  ServiceAccount:        │           │  ClusterRoleBinding:    │
//	│    provider-operator    │──────────►│    subjects:            │
//	│                         │ Global SA │    - kind: User         │
//	│  JWT contains:          │  Format   │      name: system:kcp:  │
//	│    ClusterNameKey       │           │        serviceaccount:  │
//	│                         │           │        <provider-cluster│
//	│                         │           │        >:default:       │
//	│                         │           │        provider-operator│
//	└─────────────────────────┘           └─────────────────────────┘
package apiresourceschema

const (
	// VirtualWorkspaceName is the name of the virtual workspace.
	VirtualWorkspaceName = "apiresourceschema"
)
