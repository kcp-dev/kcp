/*
Copyright 2022 The kcp Authors.

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

package plugin

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func TestSnapshot(t *testing.T) {
	streams, stdin, stdout, _ := genericclioptions.NewTestIOStreams()

	opts := NewSnapshotOptions(streams)
	opts.Prefix = "testing"
	opts.Filename = "-"

	n, err := stdin.WriteString(multiCRDYaml)
	require.NoError(t, err)
	require.Equal(t, len(multiCRDYaml), n)

	err = opts.Validate()
	require.NoError(t, err)

	err = opts.Complete()
	require.NoError(t, err)

	err = opts.Run()
	require.NoError(t, err)

	require.Empty(t, cmp.Diff(expectedYAML, strings.Trim(stdout.String(), "\n")))
}

//nolint:dupword
var multiCRDYaml = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.5.0
  name: endpoints.core
spec:
  group: ""
  names:
    kind: Endpoints
    listKind: EndpointsList
    plural: endpoints
    singular: endpoints
  scope: Namespaced
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        description: 'Endpoints is a collection of endpoints that implement the actual service. Example:   Name: "mysvc",   Subsets: [     {       Addresses: [{"ip": "10.10.1.1"}, {"ip": "10.10.2.2"}],       Ports: [{"name": "a", "port": 8675}, {"name": "b", "port": 309}]     },     {       Addresses: [{"ip": "10.10.3.3"}],       Ports: [{"name": "a", "port": 93}, {"name": "b", "port": 76}]     },  ]'
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          subsets:
            description: The set of all endpoints is the union of all subsets. Addresses are placed into subsets according to the IPs they share. A single address with multiple ports, some of which are ready and some of which are not (because they come from different containers) will result in the address being displayed in different subsets for the different ports. No address will appear in both Addresses and NotReadyAddresses in the same subset. Sets of addresses and ports that comprise a service.
            items:
              description: 'EndpointSubset is a group of addresses with a common set of ports. The expanded set of endpoints is the Cartesian product of Addresses x Ports. For example, given:   {     Addresses: [{"ip": "10.10.1.1"}, {"ip": "10.10.2.2"}],     Ports:     [{"name": "a", "port": 8675}, {"name": "b", "port": 309}]   } The resulting set of endpoints can be viewed as:     a: [ 10.10.1.1:8675, 10.10.2.2:8675 ],     b: [ 10.10.1.1:309, 10.10.2.2:309 ]'
              properties:
                addresses:
                  description: IP addresses which offer the related ports that are marked as ready. These endpoints should be considered safe for load balancers and clients to utilize.
                  items:
                    description: EndpointAddress is a tuple that describes single IP address.
                    properties:
                      hostname:
                        description: The Hostname of this endpoint
                        type: string
                      ip:
                        description: 'The IP of this endpoint. May not be loopback (127.0.0.0/8), link-local (169.254.0.0/16), or link-local multicast ((224.0.0.0/24). IPv6 is also accepted but not fully supported on all platforms. Also, certain kubernetes components, like kube-proxy, are not IPv6 ready. TODO: This should allow hostname or IP, See #4447.'
                        type: string
                      nodeName:
                        description: 'Optional: Node hosting this endpoint. This can be used to determine endpoints local to a node.'
                        type: string
                      targetRef:
                        description: Reference to object providing the endpoint.
                        properties:
                          apiVersion:
                            description: API version of the referent.
                            type: string
                          fieldPath:
                            description: 'If referring to a piece of an object instead of an entire object, this string should contain a valid JSON/Go field access statement, such as desiredState.manifest.containers[2]. For example, if the object reference is to a container within a pod, this would take on a value like: "spec.containers{name}" (where "name" refers to the name of the container that triggered the event) or if no container name is specified "spec.containers[2]" (container with index 2 in this pod). This syntax is chosen only to have some well-defined way of referencing a part of an object. TODO: this design is not final and this field is subject to change in the future.'
                            type: string
                          kind:
                            description: 'Kind of the referent. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
                            type: string
                          name:
                            description: 'Name of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names'
                            type: string
                          namespace:
                            description: 'Namespace of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/'
                            type: string
                          resourceVersion:
                            description: 'Specific resourceVersion to which this reference is made, if any. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#concurrency-control-and-consistency'
                            type: string
                          uid:
                            description: 'UID of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#uids'
                            type: string
                        type: object
                    required:
                    - ip
                    type: object
                  type: array
                notReadyAddresses:
                  description: IP addresses which offer the related ports but are not currently marked as ready because they have not yet finished starting, have recently failed a readiness check, or have recently failed a liveness check.
                  items:
                    description: EndpointAddress is a tuple that describes single IP address.
                    properties:
                      hostname:
                        description: The Hostname of this endpoint
                        type: string
                      ip:
                        description: 'The IP of this endpoint. May not be loopback (127.0.0.0/8), link-local (169.254.0.0/16), or link-local multicast ((224.0.0.0/24). IPv6 is also accepted but not fully supported on all platforms. Also, certain kubernetes components, like kube-proxy, are not IPv6 ready. TODO: This should allow hostname or IP, See #4447.'
                        type: string
                      nodeName:
                        description: 'Optional: Node hosting this endpoint. This can be used to determine endpoints local to a node.'
                        type: string
                      targetRef:
                        description: Reference to object providing the endpoint.
                        properties:
                          apiVersion:
                            description: API version of the referent.
                            type: string
                          fieldPath:
                            description: 'If referring to a piece of an object instead of an entire object, this string should contain a valid JSON/Go field access statement, such as desiredState.manifest.containers[2]. For example, if the object reference is to a container within a pod, this would take on a value like: "spec.containers{name}" (where "name" refers to the name of the container that triggered the event) or if no container name is specified "spec.containers[2]" (container with index 2 in this pod). This syntax is chosen only to have some well-defined way of referencing a part of an object. TODO: this design is not final and this field is subject to change in the future.'
                            type: string
                          kind:
                            description: 'Kind of the referent. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
                            type: string
                          name:
                            description: 'Name of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names'
                            type: string
                          namespace:
                            description: 'Namespace of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/'
                            type: string
                          resourceVersion:
                            description: 'Specific resourceVersion to which this reference is made, if any. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#concurrency-control-and-consistency'
                            type: string
                          uid:
                            description: 'UID of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#uids'
                            type: string
                        type: object
                    required:
                    - ip
                    type: object
                  type: array
                ports:
                  description: Port numbers available on the related IP addresses.
                  items:
                    description: EndpointPort is a tuple that describes a single port.
                    properties:
                      appProtocol:
                        description: The application protocol for this port. This field follows standard Kubernetes label syntax. Un-prefixed names are reserved for IANA standard service names (as per RFC-6335 and http://www.iana.org/assignments/service-names). Non-standard protocols should use prefixed names such as mycompany.com/my-custom-protocol. Field can be enabled with ServiceAppProtocol feature gate.
                        type: string
                      name:
                        description: The name of this port.  This must match the 'name' field in the corresponding ServicePort. Must be a DNS_LABEL. Optional only if one port is defined.
                        type: string
                      port:
                        description: The port number of the endpoint.
                        format: int32
                        type: integer
                      protocol:
                        default: TCP
                        description: The IP protocol for this port. Must be UDP, TCP, or SCTP. Default is TCP.
                        type: string
                    required:
                    - port
                    type: object
                  type: array
              type: object
            type: array
        type: object
    served: true
    storage: true
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.5.0
  name: services.core
spec:
  group: ""
  names:
    kind: Service
    listKind: ServiceList
    plural: services
    singular: service
  scope: Namespaced
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        description: Service is a named abstraction of software service (for example, mysql) consisting of local port (for example 3306) that the proxy listens on, and the selector that determines which pods will answer requests sent through the proxy.
        properties:
          apiVersion:
            description: 'APIVersion defines the versioned schema of this representation of an object. Servers should convert recognized schemas to the latest internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
            type: string
          kind:
            description: 'Kind is a string value representing the REST resource this object represents. Servers may infer this from the endpoint the client submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
            type: string
          metadata:
            type: object
          spec:
            description: Spec defines the behavior of a service. https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
            properties:
              clusterIP:
                description: 'clusterIP is the IP address of the service and is usually assigned randomly by the master. If an address is specified manually and is not in use by others, it will be allocated to the service; otherwise, creation of the service will fail. This field can not be changed through updates. Valid values are "None", empty string (""), or a valid IP address. "None" can be specified for headless services when proxying is not required. Only applies to types ClusterIP, NodePort, and LoadBalancer. Ignored if type is ExternalName. More info: https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies'
                type: string
              externalIPs:
                description: externalIPs is a list of IP addresses for which nodes in the cluster will also accept traffic for this service.  These IPs are not managed by Kubernetes.  The user is responsible for ensuring that traffic arrives at a node with this IP.  A common example is external load-balancers that are not part of the Kubernetes system.
                items:
                  type: string
                type: array
              externalName:
                description: externalName is the external reference that kubedns or equivalent will return as a CNAME record for this service. No proxying will be involved. Must be a valid RFC-1123 hostname (https://tools.ietf.org/html/rfc1123) and requires Type to be ExternalName.
                type: string
              externalTrafficPolicy:
                description: externalTrafficPolicy denotes if this Service desires to route external traffic to node-local or cluster-wide endpoints. "Local" preserves the client source IP and avoids a second hop for LoadBalancer and Nodeport type services, but risks potentially imbalanced traffic spreading. "Cluster" obscures the client source IP and may cause a second hop to another node, but should have good overall load-spreading.
                type: string
              healthCheckNodePort:
                description: healthCheckNodePort specifies the healthcheck nodePort for the service. If not specified, HealthCheckNodePort is created by the service api backend with the allocated nodePort. Will use user-specified nodePort value if specified by the client. Only effects when Type is set to LoadBalancer and ExternalTrafficPolicy is set to Local.
                format: int32
                type: integer
              ipFamily:
                description: ipFamily specifies whether this Service has a preference for a particular IP family (e.g. IPv4 vs. IPv6).  If a specific IP family is requested, the clusterIP field will be allocated from that family, if it is available in the cluster.  If no IP family is requested, the cluster's primary IP family will be used. Other IP fields (loadBalancerIP, loadBalancerSourceRanges, externalIPs) and controllers which allocate external load-balancers should use the same IP family.  Endpoints for this Service will be of this family.  This field is immutable after creation. Assigning a ServiceIPFamily not available in the cluster (e.g. IPv6 in IPv4 only cluster) is an error condition and will fail during clusterIP assignment.
                type: string
              loadBalancerIP:
                description: 'Only applies to Service Type: LoadBalancer LoadBalancer will get created with the IP specified in this field. This feature depends on whether the underlying cloud-provider supports specifying the loadBalancerIP when a load balancer is created. This field will be ignored if the cloud-provider does not support the feature.'
                type: string
              loadBalancerSourceRanges:
                description: 'If specified and supported by the platform, this will restrict traffic through the cloud-provider load-balancer will be restricted to the specified client IPs. This field will be ignored if the cloud-provider does not support the feature." More info: https://kubernetes.io/docs/tasks/access-application-cluster/configure-cloud-provider-firewall/'
                items:
                  type: string
                type: array
              ports:
                description: 'The list of ports that are exposed by this service. More info: https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies'
                items:
                  description: ServicePort contains information on service's port.
                  properties:
                    appProtocol:
                      description: The application protocol for this port. This field follows standard Kubernetes label syntax. Un-prefixed names are reserved for IANA standard service names (as per RFC-6335 and http://www.iana.org/assignments/service-names). Non-standard protocols should use prefixed names such as mycompany.com/my-custom-protocol. Field can be enabled with ServiceAppProtocol feature gate.
                      type: string
                    name:
                      description: The name of this port within the service. This must be a DNS_LABEL. All ports within a ServiceSpec must have unique names. When considering the endpoints for a Service, this must match the 'name' field in the EndpointPort. Optional if only one ServicePort is defined on this service.
                      type: string
                    nodePort:
                      description: 'The port on each node on which this service is exposed when type=NodePort or LoadBalancer. Usually assigned by the system. If specified, it will be allocated to the service if unused or else creation of the service will fail. Default is to auto-allocate a port if the ServiceType of this Service requires one. More info: https://kubernetes.io/docs/concepts/services-networking/service/#type-nodeport'
                      format: int32
                      type: integer
                    port:
                      description: The port that will be exposed by this service.
                      format: int32
                      type: integer
                    protocol:
                      default: TCP
                      description: The IP protocol for this port. Supports "TCP", "UDP", and "SCTP". Default is TCP.
                      type: string
                    targetPort:
                      anyOf:
                      - type: integer
                      - type: string
                      description: 'Number or name of the port to access on the pods targeted by the service. Number must be in the range 1 to 65535. Name must be an IANA_SVC_NAME. If this is a string, it will be looked up as a named port in the target Pod''s container ports. If this is not specified, the value of the ''port'' field is used (an identity map). This field is ignored for services with clusterIP=None, and should be omitted or set equal to the ''port'' field. More info: https://kubernetes.io/docs/concepts/services-networking/service/#defining-a-service'
                      x-kubernetes-int-or-string: true
                  required:
                  - port
                  type: object
                type: array
                x-kubernetes-list-map-keys:
                - port
                - protocol
                x-kubernetes-list-type: map
              publishNotReadyAddresses:
                description: publishNotReadyAddresses, when set to true, indicates that DNS implementations must publish the notReadyAddresses of subsets for the Endpoints associated with the Service. The default value is false. The primary use case for setting this field is to use a StatefulSet's Headless Service to propagate SRV records for its Pods without respect to their readiness for purpose of peer discovery.
                type: boolean
              selector:
                additionalProperties:
                  type: string
                description: 'Route service traffic to pods with label keys and values matching this selector. If empty or not present, the service is assumed to have an external process managing its endpoints, which Kubernetes will not modify. Only applies to types ClusterIP, NodePort, and LoadBalancer. Ignored if type is ExternalName. More info: https://kubernetes.io/docs/concepts/services-networking/service/'
                type: object
              sessionAffinity:
                description: 'Supports "ClientIP" and "None". Used to maintain session affinity. Enable client IP based session affinity. Must be ClientIP or None. Defaults to None. More info: https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies'
                type: string
              sessionAffinityConfig:
                description: sessionAffinityConfig contains the configurations of session affinity.
                properties:
                  clientIP:
                    description: clientIP contains the configurations of Client IP based session affinity.
                    properties:
                      timeoutSeconds:
                        description: timeoutSeconds specifies the seconds of ClientIP type session sticky time. The value must be >0 && <=86400(for 1 day) if ServiceAffinity == "ClientIP". Default value is 10800(for 3 hours).
                        format: int32
                        type: integer
                    type: object
                type: object
              topologyKeys:
                description: topologyKeys is a preference-order list of topology keys which implementations of services should use to preferentially sort endpoints when accessing this Service, it can not be used at the same time as externalTrafficPolicy=Local. Topology keys must be valid label keys and at most 16 keys may be specified. Endpoints are chosen based on the first topology key with available backends. If this field is specified and all entries have no backends that match the topology of the client, the service has no backends for that client and connections should fail. The special value "*" may be used to mean "any topology". This catch-all value, if used, only makes sense as the last value in the list. If this is not specified or empty, no topology constraints will be applied.
                items:
                  type: string
                type: array
              type:
                description: 'type determines how the Service is exposed. Defaults to ClusterIP. Valid options are ExternalName, ClusterIP, NodePort, and LoadBalancer. "ExternalName" maps to the specified externalName. "ClusterIP" allocates a cluster-internal IP address for load-balancing to endpoints. Endpoints are determined by the selector or if that is not specified, by manual construction of an Endpoints object. If clusterIP is "None", no virtual IP is allocated and the endpoints are published as a set of endpoints rather than a stable IP. "NodePort" builds on ClusterIP and allocates a port on every node which routes to the clusterIP. "LoadBalancer" builds on NodePort and creates an external load-balancer (if supported in the current cloud) which routes to the clusterIP. More info: https://kubernetes.io/docs/concepts/services-networking/service/#publishing-services-service-types'
                type: string
            type: object
          status:
            description: 'Most recently observed status of the service. Populated by the system. Read-only. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status'
            properties:
              loadBalancer:
                description: LoadBalancer contains the current status of the load-balancer, if one is present.
                properties:
                  ingress:
                    description: Ingress is a list containing ingress points for the load-balancer. Traffic intended for the service should be sent to these ingress points.
                    items:
                      description: 'LoadBalancerIngress represents the status of a load-balancer ingress point: traffic intended for the service should be sent to an ingress point.'
                      properties:
                        hostname:
                          description: Hostname is set for load-balancer ingress points that are DNS based (typically AWS load-balancers)
                          type: string
                        ip:
                          description: IP is set for load-balancer ingress points that are IP based (typically GCE or OpenStack load-balancers)
                          type: string
                      type: object
                    type: array
                type: object
            type: object
        type: object
    served: true
    storage: true
    subresources:
      status: {}
`

//nolint:dupword
var expectedYAML = `apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: testing.endpoints.core
spec:
  conversion:
    strategy: None
  group: ""
  names:
    kind: Endpoints
    listKind: EndpointsList
    plural: endpoints
    singular: endpoints
  scope: Namespaced
  versions:
  - name: v1
    schema:
      description: 'Endpoints is a collection of endpoints that implement the actual
        service. Example:   Name: "mysvc",   Subsets: [     {       Addresses: [{"ip":
        "10.10.1.1"}, {"ip": "10.10.2.2"}],       Ports: [{"name": "a", "port": 8675},
        {"name": "b", "port": 309}]     },     {       Addresses: [{"ip": "10.10.3.3"}],       Ports:
        [{"name": "a", "port": 93}, {"name": "b", "port": 76}]     },  ]'
      properties:
        apiVersion:
          description: 'APIVersion defines the versioned schema of this representation
            of an object. Servers should convert recognized schemas to the latest
            internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
          type: string
        kind:
          description: 'Kind is a string value representing the REST resource this
            object represents. Servers may infer this from the endpoint the client
            submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
          type: string
        metadata:
          type: object
        subsets:
          description: The set of all endpoints is the union of all subsets. Addresses
            are placed into subsets according to the IPs they share. A single address
            with multiple ports, some of which are ready and some of which are not
            (because they come from different containers) will result in the address
            being displayed in different subsets for the different ports. No address
            will appear in both Addresses and NotReadyAddresses in the same subset.
            Sets of addresses and ports that comprise a service.
          items:
            description: 'EndpointSubset is a group of addresses with a common set
              of ports. The expanded set of endpoints is the Cartesian product of
              Addresses x Ports. For example, given:   {     Addresses: [{"ip": "10.10.1.1"},
              {"ip": "10.10.2.2"}],     Ports:     [{"name": "a", "port": 8675}, {"name":
              "b", "port": 309}]   } The resulting set of endpoints can be viewed
              as:     a: [ 10.10.1.1:8675, 10.10.2.2:8675 ],     b: [ 10.10.1.1:309,
              10.10.2.2:309 ]'
            properties:
              addresses:
                description: IP addresses which offer the related ports that are marked
                  as ready. These endpoints should be considered safe for load balancers
                  and clients to utilize.
                items:
                  description: EndpointAddress is a tuple that describes single IP
                    address.
                  properties:
                    hostname:
                      description: The Hostname of this endpoint
                      type: string
                    ip:
                      description: 'The IP of this endpoint. May not be loopback (127.0.0.0/8),
                        link-local (169.254.0.0/16), or link-local multicast ((224.0.0.0/24).
                        IPv6 is also accepted but not fully supported on all platforms.
                        Also, certain kubernetes components, like kube-proxy, are
                        not IPv6 ready. TODO: This should allow hostname or IP, See
                        #4447.'
                      type: string
                    nodeName:
                      description: 'Optional: Node hosting this endpoint. This can
                        be used to determine endpoints local to a node.'
                      type: string
                    targetRef:
                      description: Reference to object providing the endpoint.
                      properties:
                        apiVersion:
                          description: API version of the referent.
                          type: string
                        fieldPath:
                          description: 'If referring to a piece of an object instead
                            of an entire object, this string should contain a valid
                            JSON/Go field access statement, such as desiredState.manifest.containers[2].
                            For example, if the object reference is to a container
                            within a pod, this would take on a value like: "spec.containers{name}"
                            (where "name" refers to the name of the container that
                            triggered the event) or if no container name is specified
                            "spec.containers[2]" (container with index 2 in this pod).
                            This syntax is chosen only to have some well-defined way
                            of referencing a part of an object. TODO: this design
                            is not final and this field is subject to change in the
                            future.'
                          type: string
                        kind:
                          description: 'Kind of the referent. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
                          type: string
                        name:
                          description: 'Name of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names'
                          type: string
                        namespace:
                          description: 'Namespace of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/'
                          type: string
                        resourceVersion:
                          description: 'Specific resourceVersion to which this reference
                            is made, if any. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#concurrency-control-and-consistency'
                          type: string
                        uid:
                          description: 'UID of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#uids'
                          type: string
                      type: object
                  required:
                  - ip
                  type: object
                type: array
              notReadyAddresses:
                description: IP addresses which offer the related ports but are not
                  currently marked as ready because they have not yet finished starting,
                  have recently failed a readiness check, or have recently failed
                  a liveness check.
                items:
                  description: EndpointAddress is a tuple that describes single IP
                    address.
                  properties:
                    hostname:
                      description: The Hostname of this endpoint
                      type: string
                    ip:
                      description: 'The IP of this endpoint. May not be loopback (127.0.0.0/8),
                        link-local (169.254.0.0/16), or link-local multicast ((224.0.0.0/24).
                        IPv6 is also accepted but not fully supported on all platforms.
                        Also, certain kubernetes components, like kube-proxy, are
                        not IPv6 ready. TODO: This should allow hostname or IP, See
                        #4447.'
                      type: string
                    nodeName:
                      description: 'Optional: Node hosting this endpoint. This can
                        be used to determine endpoints local to a node.'
                      type: string
                    targetRef:
                      description: Reference to object providing the endpoint.
                      properties:
                        apiVersion:
                          description: API version of the referent.
                          type: string
                        fieldPath:
                          description: 'If referring to a piece of an object instead
                            of an entire object, this string should contain a valid
                            JSON/Go field access statement, such as desiredState.manifest.containers[2].
                            For example, if the object reference is to a container
                            within a pod, this would take on a value like: "spec.containers{name}"
                            (where "name" refers to the name of the container that
                            triggered the event) or if no container name is specified
                            "spec.containers[2]" (container with index 2 in this pod).
                            This syntax is chosen only to have some well-defined way
                            of referencing a part of an object. TODO: this design
                            is not final and this field is subject to change in the
                            future.'
                          type: string
                        kind:
                          description: 'Kind of the referent. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
                          type: string
                        name:
                          description: 'Name of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names'
                          type: string
                        namespace:
                          description: 'Namespace of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/'
                          type: string
                        resourceVersion:
                          description: 'Specific resourceVersion to which this reference
                            is made, if any. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#concurrency-control-and-consistency'
                          type: string
                        uid:
                          description: 'UID of the referent. More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#uids'
                          type: string
                      type: object
                  required:
                  - ip
                  type: object
                type: array
              ports:
                description: Port numbers available on the related IP addresses.
                items:
                  description: EndpointPort is a tuple that describes a single port.
                  properties:
                    appProtocol:
                      description: The application protocol for this port. This field
                        follows standard Kubernetes label syntax. Un-prefixed names
                        are reserved for IANA standard service names (as per RFC-6335
                        and http://www.iana.org/assignments/service-names). Non-standard
                        protocols should use prefixed names such as mycompany.com/my-custom-protocol.
                        Field can be enabled with ServiceAppProtocol feature gate.
                      type: string
                    name:
                      description: The name of this port.  This must match the 'name'
                        field in the corresponding ServicePort. Must be a DNS_LABEL.
                        Optional only if one port is defined.
                      type: string
                    port:
                      description: The port number of the endpoint.
                      format: int32
                      type: integer
                    protocol:
                      default: TCP
                      description: The IP protocol for this port. Must be UDP, TCP,
                        or SCTP. Default is TCP.
                      type: string
                  required:
                  - port
                  type: object
                type: array
            type: object
          type: array
      type: object
    served: true
    storage: true
    subresources: {}

---
apiVersion: apis.kcp.io/v1alpha1
kind: APIResourceSchema
metadata:
  name: testing.services.core
spec:
  conversion:
    strategy: None
  group: ""
  names:
    kind: Service
    listKind: ServiceList
    plural: services
    singular: service
  scope: Namespaced
  versions:
  - name: v1
    schema:
      description: Service is a named abstraction of software service (for example,
        mysql) consisting of local port (for example 3306) that the proxy listens
        on, and the selector that determines which pods will answer requests sent
        through the proxy.
      properties:
        apiVersion:
          description: 'APIVersion defines the versioned schema of this representation
            of an object. Servers should convert recognized schemas to the latest
            internal value, and may reject unrecognized values. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources'
          type: string
        kind:
          description: 'Kind is a string value representing the REST resource this
            object represents. Servers may infer this from the endpoint the client
            submits requests to. Cannot be updated. In CamelCase. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds'
          type: string
        metadata:
          type: object
        spec:
          description: Spec defines the behavior of a service. https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status
          properties:
            clusterIP:
              description: 'clusterIP is the IP address of the service and is usually
                assigned randomly by the master. If an address is specified manually
                and is not in use by others, it will be allocated to the service;
                otherwise, creation of the service will fail. This field can not be
                changed through updates. Valid values are "None", empty string (""),
                or a valid IP address. "None" can be specified for headless services
                when proxying is not required. Only applies to types ClusterIP, NodePort,
                and LoadBalancer. Ignored if type is ExternalName. More info: https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies'
              type: string
            externalIPs:
              description: externalIPs is a list of IP addresses for which nodes in
                the cluster will also accept traffic for this service.  These IPs
                are not managed by Kubernetes.  The user is responsible for ensuring
                that traffic arrives at a node with this IP.  A common example is
                external load-balancers that are not part of the Kubernetes system.
              items:
                type: string
              type: array
            externalName:
              description: externalName is the external reference that kubedns or
                equivalent will return as a CNAME record for this service. No proxying
                will be involved. Must be a valid RFC-1123 hostname (https://tools.ietf.org/html/rfc1123)
                and requires Type to be ExternalName.
              type: string
            externalTrafficPolicy:
              description: externalTrafficPolicy denotes if this Service desires to
                route external traffic to node-local or cluster-wide endpoints. "Local"
                preserves the client source IP and avoids a second hop for LoadBalancer
                and Nodeport type services, but risks potentially imbalanced traffic
                spreading. "Cluster" obscures the client source IP and may cause a
                second hop to another node, but should have good overall load-spreading.
              type: string
            healthCheckNodePort:
              description: healthCheckNodePort specifies the healthcheck nodePort
                for the service. If not specified, HealthCheckNodePort is created
                by the service api backend with the allocated nodePort. Will use user-specified
                nodePort value if specified by the client. Only effects when Type
                is set to LoadBalancer and ExternalTrafficPolicy is set to Local.
              format: int32
              type: integer
            ipFamily:
              description: ipFamily specifies whether this Service has a preference
                for a particular IP family (e.g. IPv4 vs. IPv6).  If a specific IP
                family is requested, the clusterIP field will be allocated from that
                family, if it is available in the cluster.  If no IP family is requested,
                the cluster's primary IP family will be used. Other IP fields (loadBalancerIP,
                loadBalancerSourceRanges, externalIPs) and controllers which allocate
                external load-balancers should use the same IP family.  Endpoints
                for this Service will be of this family.  This field is immutable
                after creation. Assigning a ServiceIPFamily not available in the cluster
                (e.g. IPv6 in IPv4 only cluster) is an error condition and will fail
                during clusterIP assignment.
              type: string
            loadBalancerIP:
              description: 'Only applies to Service Type: LoadBalancer LoadBalancer
                will get created with the IP specified in this field. This feature
                depends on whether the underlying cloud-provider supports specifying
                the loadBalancerIP when a load balancer is created. This field will
                be ignored if the cloud-provider does not support the feature.'
              type: string
            loadBalancerSourceRanges:
              description: 'If specified and supported by the platform, this will
                restrict traffic through the cloud-provider load-balancer will be
                restricted to the specified client IPs. This field will be ignored
                if the cloud-provider does not support the feature." More info: https://kubernetes.io/docs/tasks/access-application-cluster/configure-cloud-provider-firewall/'
              items:
                type: string
              type: array
            ports:
              description: 'The list of ports that are exposed by this service. More
                info: https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies'
              items:
                description: ServicePort contains information on service's port.
                properties:
                  appProtocol:
                    description: The application protocol for this port. This field
                      follows standard Kubernetes label syntax. Un-prefixed names
                      are reserved for IANA standard service names (as per RFC-6335
                      and http://www.iana.org/assignments/service-names). Non-standard
                      protocols should use prefixed names such as mycompany.com/my-custom-protocol.
                      Field can be enabled with ServiceAppProtocol feature gate.
                    type: string
                  name:
                    description: The name of this port within the service. This must
                      be a DNS_LABEL. All ports within a ServiceSpec must have unique
                      names. When considering the endpoints for a Service, this must
                      match the 'name' field in the EndpointPort. Optional if only
                      one ServicePort is defined on this service.
                    type: string
                  nodePort:
                    description: 'The port on each node on which this service is exposed
                      when type=NodePort or LoadBalancer. Usually assigned by the
                      system. If specified, it will be allocated to the service if
                      unused or else creation of the service will fail. Default is
                      to auto-allocate a port if the ServiceType of this Service requires
                      one. More info: https://kubernetes.io/docs/concepts/services-networking/service/#type-nodeport'
                    format: int32
                    type: integer
                  port:
                    description: The port that will be exposed by this service.
                    format: int32
                    type: integer
                  protocol:
                    default: TCP
                    description: The IP protocol for this port. Supports "TCP", "UDP",
                      and "SCTP". Default is TCP.
                    type: string
                  targetPort:
                    anyOf:
                    - type: integer
                    - type: string
                    description: 'Number or name of the port to access on the pods
                      targeted by the service. Number must be in the range 1 to 65535.
                      Name must be an IANA_SVC_NAME. If this is a string, it will
                      be looked up as a named port in the target Pod''s container
                      ports. If this is not specified, the value of the ''port'' field
                      is used (an identity map). This field is ignored for services
                      with clusterIP=None, and should be omitted or set equal to the
                      ''port'' field. More info: https://kubernetes.io/docs/concepts/services-networking/service/#defining-a-service'
                    x-kubernetes-int-or-string: true
                required:
                - port
                type: object
              type: array
              x-kubernetes-list-map-keys:
              - port
              - protocol
              x-kubernetes-list-type: map
            publishNotReadyAddresses:
              description: publishNotReadyAddresses, when set to true, indicates that
                DNS implementations must publish the notReadyAddresses of subsets
                for the Endpoints associated with the Service. The default value is
                false. The primary use case for setting this field is to use a StatefulSet's
                Headless Service to propagate SRV records for its Pods without respect
                to their readiness for purpose of peer discovery.
              type: boolean
            selector:
              additionalProperties:
                type: string
              description: 'Route service traffic to pods with label keys and values
                matching this selector. If empty or not present, the service is assumed
                to have an external process managing its endpoints, which Kubernetes
                will not modify. Only applies to types ClusterIP, NodePort, and LoadBalancer.
                Ignored if type is ExternalName. More info: https://kubernetes.io/docs/concepts/services-networking/service/'
              type: object
            sessionAffinity:
              description: 'Supports "ClientIP" and "None". Used to maintain session
                affinity. Enable client IP based session affinity. Must be ClientIP
                or None. Defaults to None. More info: https://kubernetes.io/docs/concepts/services-networking/service/#virtual-ips-and-service-proxies'
              type: string
            sessionAffinityConfig:
              description: sessionAffinityConfig contains the configurations of session
                affinity.
              properties:
                clientIP:
                  description: clientIP contains the configurations of Client IP based
                    session affinity.
                  properties:
                    timeoutSeconds:
                      description: timeoutSeconds specifies the seconds of ClientIP
                        type session sticky time. The value must be >0 && <=86400(for
                        1 day) if ServiceAffinity == "ClientIP". Default value is
                        10800(for 3 hours).
                      format: int32
                      type: integer
                  type: object
              type: object
            topologyKeys:
              description: topologyKeys is a preference-order list of topology keys
                which implementations of services should use to preferentially sort
                endpoints when accessing this Service, it can not be used at the same
                time as externalTrafficPolicy=Local. Topology keys must be valid label
                keys and at most 16 keys may be specified. Endpoints are chosen based
                on the first topology key with available backends. If this field is
                specified and all entries have no backends that match the topology
                of the client, the service has no backends for that client and connections
                should fail. The special value "*" may be used to mean "any topology".
                This catch-all value, if used, only makes sense as the last value
                in the list. If this is not specified or empty, no topology constraints
                will be applied.
              items:
                type: string
              type: array
            type:
              description: 'type determines how the Service is exposed. Defaults to
                ClusterIP. Valid options are ExternalName, ClusterIP, NodePort, and
                LoadBalancer. "ExternalName" maps to the specified externalName. "ClusterIP"
                allocates a cluster-internal IP address for load-balancing to endpoints.
                Endpoints are determined by the selector or if that is not specified,
                by manual construction of an Endpoints object. If clusterIP is "None",
                no virtual IP is allocated and the endpoints are published as a set
                of endpoints rather than a stable IP. "NodePort" builds on ClusterIP
                and allocates a port on every node which routes to the clusterIP.
                "LoadBalancer" builds on NodePort and creates an external load-balancer
                (if supported in the current cloud) which routes to the clusterIP.
                More info: https://kubernetes.io/docs/concepts/services-networking/service/#publishing-services-service-types'
              type: string
          type: object
        status:
          description: 'Most recently observed status of the service. Populated by
            the system. Read-only. More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#spec-and-status'
          properties:
            loadBalancer:
              description: LoadBalancer contains the current status of the load-balancer,
                if one is present.
              properties:
                ingress:
                  description: Ingress is a list containing ingress points for the
                    load-balancer. Traffic intended for the service should be sent
                    to these ingress points.
                  items:
                    description: 'LoadBalancerIngress represents the status of a load-balancer
                      ingress point: traffic intended for the service should be sent
                      to an ingress point.'
                    properties:
                      hostname:
                        description: Hostname is set for load-balancer ingress points
                          that are DNS based (typically AWS load-balancers)
                        type: string
                      ip:
                        description: IP is set for load-balancer ingress points that
                          are IP based (typically GCE or OpenStack load-balancers)
                        type: string
                    type: object
                  type: array
              type: object
          type: object
      type: object
    served: true
    storage: true
    subresources:
      status: {}

---`
