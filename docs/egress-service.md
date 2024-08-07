# Egress Service

## Introduction

The Egress Service feature enables the egress traffic of pods backing a LoadBalancer service to exit the cluster using its ingress IP.
This is useful for external systems that communicate with applications running on the Kubernetes cluster through a LoadBalancer service and expect that the source IP of egress traffic originating from the pods backing the service is identical to the destination IP they use to reach them - i.e the LoadBalancer's ingress IP.

This functionality can be toggled by annotating a LoadBalancer service, making the source IP of egress packets originating from all of the non host-networked pods that are endpoints of it to be its ingress IP.
Announcing the service externally (for ingress traffic) is handled by a LoadBalancer provider (like MetalLB) and not by OVN-Kubernetes as explained later.

## Details

Only SNATing a pod's IP to the LoadBalancer service ingress IP that it is backing is problematic, as usually the ingress IP is exposed via multiple nodes by the LoadBalancer provider. This means we can't just add an SNAT to the regular traffic flow of a pod before it exits its node because we don't have a guarantee that the reply will come back to the pod's node (where the traffic originated).
An external client usually has multiple paths to reach the LoadBalancer ingress IP and could reply to a node that is not the pod's node - in that case the other node does not have the proper CONNTRACK entries to send the reply back to the pod and the traffic is lost.
For that reason, we need to make sure that all traffic for the service's pods (ingress/egress) is handled by a single node so the right CONNTRACK entries are always matched and the traffic is not lost.

The egress part is handled by OVN-Kubernetes, which chooses a node that acts as the point of ingress/egress, and steers the relevant pods' egress traffic to its mgmt port, by using logical router policies on the `ovn_cluster_router`.
When that traffic reaches the node's mgmt port it will use its routing table and iptables before heading out.
Because of that, it takes care of adding the necessary iptables rules on the selected node to SNAT traffic exiting from these pods to the service's ingress IP.

These goals are achieved by introducing an annotation for users to set on LoadBalancer services: `k8s.ovn.org/egress-service`, which can be either empty (`'{}'`) or contain a `nodeSelector` field: `'{"nodeSelector":{"matchLabels":{"size": "large"}}}'` that allows limiting the nodes that can be selected to handle the service's traffic.
By specifying the `nodeSelector` field, only a node whose labels match the specified selectors can be selected for handling the service's traffic as explained earlier.
By not specifying the `nodeSelector` field any node in the cluster can be chosen to manage the service's traffic.
In addition, if the service's `ExternalTrafficPolicy` is set to `Local` an additional constraint is added that only a node that has an endpoint can be selected.

When a node is selected to handle the service's traffic both the service is annotated with `k8s.ovn.org/egress-service-host=<node_name>` (which is consumed by `ovnkube-node`) and the node is labeled with `egress-service.k8s.ovn.org/<svc-namespace>-<svc-name>: ""`, which can be consumed by a LoadBalancer provider to handle the ingress part.

Similarly to the EgressIP feature, once a node is selected it is checked for readiness (TCP/gRPC) to serve traffic every x seconds.
If a node fails the health check, its allocated services move to another node by removing the `egress-service.k8s.ovn.org/<svc-namespace>-<svc-name>: ""` label from it, removing the logical router policies from the cluster router, resetting the `k8s.ovn.org/egress-service-host=<node_name>` annotation on each of the services and requeuing them - causing a new node to be selected for the service.
If the node becomes not ready or its labels no longer match the service's selectors the same re-election process happens.

The ingress part is handled by a LoadBalancer provider, such as MetalLB, that needs to select the right node (and only it) for announcing the LoadBalancer service (ingress traffic) according to the `egress-service.k8s.ovn.org/<svc-namespace>-<svc-name>: ""` label set by OVN-Kubernetes.
A full example with MetalLB is detailed in [Usage Example](#Usage-Example).

Just to be clear, OVN-Kubernetes does not care which component advertises the LoadBalancer service or checks if it does it correctly - it is the user's responsibility to make sure ingress traffic arrives only to the node with the `egress-service.k8s.ovn.org/<svc-namespace>-<svc-name>: ""` label.

Assuming an Egress Service has `172.19.0.100` as its ingress IP and `ovn-worker` selected to handle all of its traffic, the egress traffic flow of an endpoint pod with the ip `10.244.1.6` on `ovn-worker2` towards an external destination (172.19.0.5) will look like:
```none
                     ┌────────────────────┐
                     │                    │
                     │external destination│
                     │    172.19.0.5      │
                     │                    │
                     └───▲────────────────┘
                         │
     5. packet reaches   │                      2. router policy rereoutes it
        the external     │                         to ovn-worker's mgmt port
        destination with │                      ┌──────────────────┐
        src ip:          │                  ┌───┤ovn cluster router│
        172.19.0.100     │                  │   └───────────▲──────┘
                         │                  │               │
                         │                  │               │1. packet to 172.19.0.5
                      ┌──┴───┐        ┌─────▼┐              │   heads to the cluster router
                   ┌──┘ eth1 └──┐  ┌──┘ mgmt └──┐           │   as usual
                   │ 172.19.0.2 │  │ 10.244.0.2 │           │
                   ├─────▲──────┴──┴─────┬──────┤           │   ┌────────────────┐
4. an iptables rule│     │   ovn-worker  │3.    │           │   │  ovn-worker2   │
   that SNATs to   │     │               │      │           │   │                │
   the service's ip│     │               │      │           │   │                │
   is hit          │     │  ┌────────┐   │      │           │   │ ┌────────────┐ │
                   │     │4.│routes +│   │      │           └───┼─┤    pod     │ │
                   │     └──┤iptables◄───┘      │               │ │ 10.244.1.6 │ │
                   │        └────────┘          │               │ └────────────┘ │
                   │                            │               │                │
                   └────────────────────────────┘               └────────────────┘
                3. from the mgmt port it hits ovn-worker's
                   routes and iptables rules
```
Notice how the packet exits `ovn-worker`'s eth1 and not breth0, as the packet goes through the host's routing table regardless of the gateway mode.

## Changes in OVN northbound database and iptables

The feature is implemented by reacting to events from `Services`, `EndpointSlices` and `Nodes` changes -
updating OVN's northbound database `Logical_Router_Policy` objects to steer the traffic to the selected node and creating iptables SNAT rules in its `OVN-KUBE-EGRESS-SVC` chain, which is called by the POSTROUTING chain of its nat table.

We'll see how the related objects are changed once a LoadBalancer is requested to act as an "Egress Service" by annotating it with the `k8s.ovn.org/egress-service` annotation in a Dual-Stack kind cluster.

We start with a clean cluster:
```
$ kubectl get nodes
NAME                STATUS   ROLES
ovn-control-plane   Ready    control-plane
ovn-worker          Ready    worker
ovn-worker2         Ready    worker
```

```
$ kubectl describe svc demo-svc
Name:                     demo-svc
Namespace:                default
Annotations:              <none>
Type:                     LoadBalancer
LoadBalancer Ingress:     5.5.5.5, 5555:5555:5555:5555:5555:5555:5555:5555
Endpoints:                10.244.0.5:8080,10.244.2.7:8080
                          fd00:10:244:1::5,fd00:10:244:3::7
```

```
$ ovn-nbctl lr-policy-list ovn_cluster_router
Routing Policies
      1004 inport == "rtos-ovn-control-plane" && ip4.dst == 172.18.0.3 /* ovn-control-plane */         reroute                10.244.2.2
      1004 inport == "rtos-ovn-control-plane" && ip6.dst == fc00:f853:ccd:e793::3 /* ovn-control-plane */         reroute          fd00:10:244:3::2
      1004 inport == "rtos-ovn-worker" && ip4.dst == 172.18.0.4 /* ovn-worker */         reroute                10.244.0.2
      1004 inport == "rtos-ovn-worker" && ip6.dst == fc00:f853:ccd:e793::4 /* ovn-worker */         reroute          fd00:10:244:1::2
      1004 inport == "rtos-ovn-worker2" && ip4.dst == 172.18.0.2 /* ovn-worker2 */         reroute                10.244.1.2
      1004 inport == "rtos-ovn-worker2" && ip6.dst == fc00:f853:ccd:e793::2 /* ovn-worker2 */         reroute          fd00:10:244:2::2
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 10.244.0.0/16           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 100.64.0.0/16           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 172.18.0.2/32           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 172.18.0.3/32           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 172.18.0.4/32           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fc00:f853:ccd:e793::2/128           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fc00:f853:ccd:e793::3/128           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fc00:f853:ccd:e793::4/128           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fd00:10:244::/48           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fd98::/64           allow

```

At this point nothing related to Egress Services is in place. It is worth noting that the "allow" policies (102's) that make sure east-west traffic is not affected for EgressIPs are present here as well - if the EgressIP feature is enabled it takes care of creating them, otherwise the "Egress Service" feature does (sharing the same logic), as we do not want Egress Services to change the behavior of east-west traffic.
Also, the policies created (seen later) for an Egress Service use a higher priority than the EgressIP ones, which means that if a pod belongs to both an EgressIP and an Egress Service the service's ingress IP will be used for the SNAT.

We now request that our service will act as an "Egress Service" by annotating it, with the constraint that only a node with the `"node-role.kubernetes.io/worker": ""` label can be selected to handle its traffic:
```
$ kubectl annotate svc demo-svc k8s.ovn.org/egress-service='{"nodeSelector":{"matchLabels":{"node-role.kubernetes.io/worker": ""}}}'
service/demo-svc annotated
```

Once the service is annotated a node is selected to handle all of its traffic (ingress/egress) as described earlier.
The service is annotated with its name, logical router policies are created on ovn_cluster_router to steer the endpoints' traffic to its mgmt port, SNAT rules are created in its iptables and it is labeled as the node in charge of the service's traffic:

The `k8s.ovn.org/egress-service-host` annotation points to `ovn-worker2`, meaning it was selected to handle the service's traffic:
```
$ kubectl describe svc demo-svc
Name:                     demo-svc
Namespace:                default
Annotations:              k8s.ovn.org/egress-service: {"nodeSelector":{"matchLabels":{"node-role.kubernetes.io/worker": ""}}}
                          k8s.ovn.org/egress-service-host: ovn-worker2
Type:                     LoadBalancer
LoadBalancer Ingress:     5.5.5.5, 5555:5555:5555:5555:5555:5555:5555:5555
Endpoints:                10.244.0.5:8080,10.244.2.7:8080
                          fd00:10:244:1::5,fd00:10:244:3::7
```

A logical router policy is created for each endpoint to steer its egress traffic towards `ovn-worker2`'s mgmt port:
```
$ ovn-nbctl lr-policy-list ovn_cluster_router
Routing Policies
       <truncated 1004's and 102's>
       101                              ip4.src == 10.244.0.5         reroute                10.244.1.2
       101                              ip4.src == 10.244.2.7         reroute                10.244.1.2
       101                        ip6.src == fd00:10:244:1::5         reroute          fd00:10:244:2::2
       101                        ip6.src == fd00:10:244:3::7         reroute          fd00:10:244:2::2
```

An SNAT rule to the service's ingress IP is created for each endpoint:
```
$ hostname
ovn-worker2

$ iptables-save
*nat
...
-A POSTROUTING -j OVN-KUBE-EGRESS-SVC
-A OVN-KUBE-EGRESS-SVC -s 10.244.0.5/32 -m comment --comment "default/demo-svc" -j SNAT --to-source 5.5.5.5
-A OVN-KUBE-EGRESS-SVC -s 10.244.2.7/32 -m comment --comment "default/demo-svc" -j SNAT --to-source 5.5.5.5
...

$ ip6tables-save
...
*nat
-A POSTROUTING -j OVN-KUBE-EGRESS-SVC
-A OVN-KUBE-EGRESS-SVC -s fd00:10:244:3::7/128 -m comment --comment "default/demo-svc" -j SNAT --to-source 5555:5555:5555:5555:5555:5555:5555:5555
-A OVN-KUBE-EGRESS-SVC -s fd00:10:244:1::5/128 -m comment --comment "default/demo-svc" -j SNAT --to-source 5555:5555:5555:5555:5555:5555:5555:5555
...
```

`ovn-worker2` is the only node holding the `egress-service.k8s.ovn.org/default-demo-svc=""` label:
```
$ kubectl get nodes -l egress-service.k8s.ovn.org/default-demo-svc=""
NAME          STATUS   ROLES
ovn-worker2   Ready    worker
```

When the endpoints of the service change, the logical router policies and iptables rules are changed accordingly.

We will now simulate a failover of the service when a node fails its health check.
By stopping `ovn-worker2`'s container we see that all of the resources "jump" to `ovn-worker`, as it is the only node left matching the `nodeSelector`:

```
$ docker stop ovn-worker2
ovn-worker2
```

The `k8s.ovn.org/egress-service-host` annotation now points to `ovn-worker`:
```
$ kubectl describe svc demo-svc
Name:                     demo-svc
Namespace:                default
Annotations:              k8s.ovn.org/egress-service: {"nodeSelector":{"matchLabels":{"node-role.kubernetes.io/worker": ""}}}
                          k8s.ovn.org/egress-service-host: ovn-worker
Type:                     LoadBalancer
LoadBalancer Ingress:     5.5.5.5, 5555:5555:5555:5555:5555:5555:5555:5555
Endpoints:                10.244.0.5:8080,10.244.2.7:8080
                          fd00:10:244:1::5,fd00:10:244:3::7
```

The reroute destination changed to `ovn-worker`'s mgmt port (10.244.1.2 -> 10.244.0.2, fd00:10:244:2::2 -> fd00:10:244:1::2):
```
$ ovn-nbctl lr-policy-list ovn_cluster_router
Routing Policies
       <truncated 1004's and 102's>
       101                              ip4.src == 10.244.0.5         reroute                10.244.0.2
       101                              ip4.src == 10.244.2.7         reroute                10.244.0.2
       101                        ip6.src == fd00:10:244:1::5         reroute          fd00:10:244:1::2
       101                        ip6.src == fd00:10:244:3::7         reroute          fd00:10:244:1::2
```

The iptables rules were created on `ovn-worker`:
```
$ hostname
ovn-worker

$ iptables-save
*nat
...
-A POSTROUTING -j OVN-KUBE-EGRESS-SVC
-A OVN-KUBE-EGRESS-SVC -s 10.244.0.5/32 -m comment --comment "default/demo-svc" -j SNAT --to-source 5.5.5.5
-A OVN-KUBE-EGRESS-SVC -s 10.244.2.7/32 -m comment --comment "default/demo-svc" -j SNAT --to-source 5.5.5.5
...

$ ip6tables-save
...
*nat
-A POSTROUTING -j OVN-KUBE-EGRESS-SVC
-A OVN-KUBE-EGRESS-SVC -s fd00:10:244:3::7/128 -m comment --comment "default/demo-svc" -j SNAT --to-source 5555:5555:5555:5555:5555:5555:5555:5555
-A OVN-KUBE-EGRESS-SVC -s fd00:10:244:1::5/128 -m comment --comment "default/demo-svc" -j SNAT --to-source 5555:5555:5555:5555:5555:5555:5555:5555
...
```

The label moved to `ovn-worker`:
```
$ kubectl get nodes -l egress-service.k8s.ovn.org/default-demo-svc=""
NAME         STATUS   ROLES
ovn-worker   Ready    worker
```

Finally, removing the annotation from the service resets the cluster to the point we started from:
```
$ kubectl annotate svc demo-svc k8s.ovn.org/egress-service-
service/demo-svc annotated
```

```
$ kubectl describe svc demo-svc
Name:                     demo-svc
Namespace:                default
Annotations:              <none>
Type:                     LoadBalancer
LoadBalancer Ingress:     5.5.5.5, 5555:5555:5555:5555:5555:5555:5555:5555
Endpoints:                10.244.0.5:8080,10.244.2.7:8080
```

```
$ ovn-nbctl lr-policy-list ovn_cluster_router
Routing Policies
      1004 inport == "rtos-ovn-control-plane" && ip4.dst == 172.18.0.3 /* ovn-control-plane */         reroute                10.244.2.2
      1004 inport == "rtos-ovn-control-plane" && ip6.dst == fc00:f853:ccd:e793::3 /* ovn-control-plane */         reroute          fd00:10:244:3::2
      1004 inport == "rtos-ovn-worker" && ip4.dst == 172.18.0.4 /* ovn-worker */         reroute                10.244.0.2
      1004 inport == "rtos-ovn-worker" && ip6.dst == fc00:f853:ccd:e793::4 /* ovn-worker */         reroute          fd00:10:244:1::2
      1004 inport == "rtos-ovn-worker2" && ip4.dst == 172.18.0.2 /* ovn-worker2 */         reroute                10.244.1.2
      1004 inport == "rtos-ovn-worker2" && ip6.dst == fc00:f853:ccd:e793::2 /* ovn-worker2 */         reroute          fd00:10:244:2::2
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 10.244.0.0/16           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 100.64.0.0/16           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 172.18.0.2/32           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 172.18.0.3/32           allow
       102 ip4.src == 10.244.0.0/16 && ip4.dst == 172.18.0.4/32           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fc00:f853:ccd:e793::2/128           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fc00:f853:ccd:e793::3/128           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fc00:f853:ccd:e793::4/128           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fd00:10:244::/48           allow
       102 ip6.src == fd00:10:244::/48 && ip6.dst == fd98::/64           allow

```

```
$ hostname
ovn-worker

$ iptables-save | grep EGRESS
:OVN-KUBE-EGRESS-SVC - [0:0]
-A POSTROUTING -j OVN-KUBE-EGRESS-SVC

$ ip6tables-save | grep EGRESS
:OVN-KUBE-EGRESS-SVC - [0:0]
-A POSTROUTING -j OVN-KUBE-EGRESS-SVC
```

```
$ kubectl get nodes -l egress-service.k8s.ovn.org/default-demo-svc=""
No resources found
```
## Usage Example

While the user does not need to know all of the details of how "Egress Services" work, they need to know that in order for a service to work properly the access to it from outside the cluster (ingress traffic) has to go only through the node labeled with the `egress-service.k8s.ovn.org/<svc-namespace>-<svc-name>: ""` label - i.e the node designated by OVN-Kubernetes to handle all of the service's traffic.
As mentioned earlier, OVN-Kubernetes does not care which component advertises the LoadBalancer service or checks if it does it correctly.

Here we look at an example of "Egress Services" using MetalLB to advertise the LoadBalancer service externally.
A user of MetalLB can follow these steps to create a LoadBalancer service whose endpoints exit the cluster with its ingress IP.
We already assume MetalLB's `BGPPeers` are configured and the sessions are established.

1. Create the IPAddressPool with the desired IP for the service. It makes sense to set `autoAssign: false` so it is not taken by another service by mistake - our service will request that pool explicitly. 
```yaml
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: example-pool
  namespace: metallb-system
spec:
  addresses:
  - 172.19.0.100/32
  autoAssign: false
```

2. Create the LoadBalancer service. We create it with 2 annotations:
- `metallb.universe.tf/address-pool` - to explicitly request the IP to be from the `example-pool`.
- `k8s.ovn.org/egress-service` - to request that all of the endpoints of the service exit the cluster with the service's ingress IP. We also provide a `nodeSelector` so that the traffic exits from a node that matches these selectors.
```yaml
apiVersion: v1
kind: Service
metadata:
  name: example-service
  namespace: some-namespace
  annotations:
    metallb.universe.tf/address-pool: example-pool
    k8s.ovn.org/egress-service: '{"nodeSelector":{"matchLabels":{"node-role.kubernetes.io/worker": ""}}}'
spec:
  selector:
    app: example
  ports:
    - name: http
      protocol: TCP
      port: 8080
      targetPort: 8080
  type: LoadBalancer
```

3. Advertise the service from the node in charge of the service's traffic. So far the service is "broken" - it is not reachable from outside the cluster and if the pods try to send traffic outside it would probably not come back as it is SNATed to an IP which is unknown.
We create the advertisements targeting only the node that is in charge of the service's traffic using the `nodeSelector` field, relying on ovn-k to label the node properly.
```yaml
apiVersion: metallb.io/v1beta1
kind: BGPAdvertisement
metadata:
  name: example-bgp-adv
  namespace: metallb-system
spec:
  ipAddressPools:
  - example-pool
  nodeSelector:
  - matchLabels:
      egress-service.k8s.ovn.org/some-namespace-example-service: ""
```
While possible to create more advertisements resources for the `example-pool`, it is the user's responsibility to make sure that the pool is advertised only by advertisements targeting the node holding the `egress-service.k8s.ovn.org/<svc-namespace>-<svc-name>: ""` label - otherwise the traffic of the service will be broken.
