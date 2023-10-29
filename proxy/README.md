# Cluster Proxy VirtualWorkspace

This is cluster proxy virtual workspace. It is used to run the cluster proxy
service in a virtual workspace.


## Dev

1. Run `make` to build the image.
2. Create a proxy virtual workspace:
   ```
   kubectl ws create proxy-demo --type proxy --enter
   ```
3. Run kubectl wproxy with output for agent:
   ```
   k wproxy create tilt --proxy-image ghcr.io/kcp-dev/kcp-proxy --output-file proxy.yaml
   ```
4. Run proxy in the kind cluster where KCP is running:
   ```
   KUBECONFIG=tilt.kubeconfig kubectl apply -f "proxy.yaml"
   ```
5. In the TILT file - enable section to reload deployed agent
   ```
   proxy_deployed = True
   ```
