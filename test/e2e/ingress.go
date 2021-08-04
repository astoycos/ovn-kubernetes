



var _ = ginkgo.Describe("e2e ingress traffic validation", func() {
	const (
		endpointHTTPPort    = 80
		endpointUDPPort     = 90
		clusterHTTPPort     = 81
		clusterUDPPort      = 91
		clientContainerName = "npclient"
	)

	f := framework.NewDefaultFramework("nodeport-ingress-test")
	endpointsSelector := map[string]string{"servicebackend": "true"}

	var endPoints []*v1.Pod
	var nodesHostnames sets.String
	maxTries := 0
	var nodes *v1.NodeList
	var newNodeAddresses []string
	var externalIpv4 string
	var externalIpv6 string

	ginkgo.Context("Validating ingress traffic", func() {
		ginkgo.BeforeEach(func() {
			endPoints = make([]*v1.Pod, 0)
			nodesHostnames = sets.NewString()

			var err error
			nodes, err = e2enode.GetBoundedReadySchedulableNodes(f.ClientSet, 3)
			framework.ExpectNoError(err)

			if len(nodes.Items) < 3 {
				framework.Failf(
					"Test requires >= 3 Ready nodes, but there are only %v nodes",
					len(nodes.Items))
			}

			ginkgo.By("Creating the endpoints pod, one for each worker")
			for _, node := range nodes.Items {
				// this create a udp / http netexec listener which is able to receive the "hostname"
				// command. We use this to validate that each endpoint is received at least once
				args := []string{
					"netexec",
					fmt.Sprintf("--http-port=%d", endpointHTTPPort),
					fmt.Sprintf("--udp-port=%d", endpointUDPPort),
				}
				pod, err := createPod(f, node.Name+"-ep", node.Name, f.Namespace.Name, []string{}, endpointsSelector, func(p *v1.Pod) {
					p.Spec.Containers[0].Args = args
				})
				framework.ExpectNoError(err)
				endPoints = append(endPoints, pod)
				nodesHostnames.Insert(pod.Name)

				// this is arbitrary and mutuated from k8s network e2e tests. We aim to hit all the endpoints at least once
				maxTries = len(endPoints)*len(endPoints) + 30
			}

			ginkgo.By("Creating an external container to send the traffic from")
			// the client uses the netexec command from the agnhost image, which is able to receive commands for poking other
			// addresses.
			externalIpv4, externalIpv6 = createClusterExternalContainer(clientContainerName, agnhostImage, []string{"--network", "kind", "-P"}, []string{"netexec", "--http-port=80"})
		})

		ginkgo.AfterEach(func() {
			deleteClusterExternalContainer(clientContainerName)
		})

		// This test validates ingress traffic to nodeports.
		// It creates a nodeport service on both udp and tcp, and creates a backend pod on each node.
		// The backend pods are using the agnhost - netexec command which replies to commands
		// with different protocols. We use the "hostname" command to have each backend pod to reply
		// with its hostname.
		// We use an external container to poke the service exposed on the node and we iterate until
		// all the hostnames are returned.
		// In case of dual stack enabled cluster, we iterate over all the nodes ips and try to hit the
		// endpoints from both each node's ips.
		ginkgo.It("Should be allowed by nodeport services", func() {
			serviceName := "nodeportsvc"
			ginkgo.By("Creating the nodeport service")
			npSpec := nodePortServiceSpecFrom(serviceName, endpointHTTPPort, endpointUDPPort, clusterHTTPPort, clusterUDPPort, endpointsSelector, v1.ServiceExternalTrafficPolicyTypeCluster)
			np, err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Create(context.Background(), npSpec, metav1.CreateOptions{})
			nodeTCPPort, nodeUDPPort := nodePortsFromService(np)
			framework.ExpectNoError(err)

			ginkgo.By("Waiting for the endpoints to pop up")
			err = framework.WaitForServiceEndpointsNum(f.ClientSet, f.Namespace.Name, serviceName, len(endPoints), time.Second, wait.ForeverTestTimeout)
			framework.ExpectNoError(err, "failed to validate endpoints for service %s in namespace: %s", serviceName, f.Namespace.Name)

			for _, protocol := range []string{"http", "udp"} {
				for _, node := range nodes.Items {
					for _, nodeAddress := range node.Status.Addresses {
						// skipping hostnames
						if !addressIsIP(nodeAddress) {
							continue
						}

						responses := sets.NewString()
						valid := false
						nodePort := nodeTCPPort
						if protocol == "udp" {
							nodePort = nodeUDPPort
						}

						ginkgo.By("Hitting the nodeport on " + node.Name + " and reaching all the endpoints " + protocol)
						for i := 0; i < maxTries; i++ {
							epHostname := pokeEndpointHostname(clientContainerName, protocol, nodeAddress.Address, nodePort)
							responses.Insert(epHostname)

							// each endpoint returns its hostname. By doing this, we validate that each ep was reached at least once.
							if responses.Equal(nodesHostnames) {
								framework.Logf("Validated node %s on address %s after %d tries", node.Name, nodeAddress.Address, i)
								valid = true
								break
							}
						}
						framework.ExpectEqual(valid, true, "Validation failed for node", node, responses, nodePort)
					}
				}
			}
		})
		// This test validates ingress traffic to nodeports with externalTrafficPolicy Set to local.
		// It creates a nodeport service on both udp and tcp, and creates a backend pod on each node.
		// The backend pod is using the agnhost - netexec command which replies to commands
		// with different protocols. We use the "hostname" and "clientip" commands to have each backend
		// pod to reply with its hostname and the request packet's srcIP.
		// We use an external container to poke the service exposed on the node and ensure that only the
		// nodeport on the node with the backend actually receives traffic and that the packet is not
		// SNATed.
		// In case of dual stack enabled cluster, we iterate over all the nodes ips and try to hit the
		// endpoints from both each node's ips.
		ginkgo.It("Should be allowed to node local cluster-networked endpoints by nodeport services with externalTrafficPolicy=local", func() {
			serviceName := "nodeportsvclocal"
			ginkgo.By("Creating the nodeport service with externalTrafficPolicy=local")
			npSpec := nodePortServiceSpecFrom(serviceName, endpointHTTPPort, endpointUDPPort, clusterHTTPPort, clusterUDPPort, endpointsSelector, v1.ServiceExternalTrafficPolicyTypeLocal)
			np, err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Create(context.Background(), npSpec, metav1.CreateOptions{})
			nodeTCPPort, nodeUDPPort := nodePortsFromService(np)
			framework.ExpectNoError(err)

			ginkgo.By("Waiting for the endpoints to pop up")
			err = framework.WaitForServiceEndpointsNum(f.ClientSet, f.Namespace.Name, serviceName, len(endPoints), time.Second, wait.ForeverTestTimeout)
			framework.ExpectNoError(err, "failed to validate endpoints for service %s in namespace: %s", serviceName, f.Namespace.Name)

			for _, protocol := range []string{"http", "udp"} {
				for _, node := range nodes.Items {
					for _, nodeAddress := range node.Status.Addresses {
						// skipping hostnames
						if !addressIsIP(nodeAddress) {
							continue
						}

						responses := sets.NewString()
						// Fill expected responses, it should hit the nodeLocal endpoints and not SNAT packet IP
						expectedResponses := sets.NewString()

						if utilnet.IsIPv6String(nodeAddress.Address) {
							expectedResponses.Insert(node.Name+"-ep", externalIpv6)
						} else {
							expectedResponses.Insert(node.Name+"-ep", externalIpv4)
						}

						valid := false
						nodePort := nodeTCPPort
						if protocol == "udp" {
							nodePort = nodeUDPPort
						}

						ginkgo.By("Hitting the nodeport on " + node.Name + " and trying to reach only the local endpoint with protocol " + protocol)

						for i := 0; i < maxTries; i++ {
							epHostname := pokeEndpointHostname(clientContainerName, protocol, nodeAddress.Address, nodePort)
							epClientIP := pokeEndpointClientIP(clientContainerName, protocol, nodeAddress.Address, nodePort)
							responses.Insert(epHostname, epClientIP)

							if responses.Equal(expectedResponses) {
								framework.Logf("Validated local endpoint on node %s with address %s, and packet src IP ", node.Name, nodeAddress.Address, epClientIP)
								valid = true
								break
							}

						}
						framework.ExpectEqual(valid, true, "Validation failed for node", node.Name, responses, nodePort)
					}
				}
			}
		})
		// This test validates ingress traffic to externalservices.
		// It creates a service on both udp and tcp and assignes all the first node's addresses as
		// external addresses. Then, creates a backend pod on each node.
		// The backend pods are using the agnhost - netexec command which replies to commands
		// with different protocols. We use the "hostname" command to have each backend pod to reply
		// with its hostname.
		// We use an external container to poke the service exposed on the node and we iterate until
		// all the hostnames are returned.
		// In case of dual stack enabled cluster, we iterate over all the node's addresses and try to hit the
		// endpoints from both each node's ips.
		ginkgo.It("Should be allowed by externalip services", func() {
			serviceName := "externalipsvc"

			// collecting all the first node's addresses
			addresses := []string{}
			for _, a := range nodes.Items[0].Status.Addresses {
				if addressIsIP(a) {
					addresses = append(addresses, a.Address)
				}
			}

			ginkgo.By("Creating the externalip service")
			externalIPsvcSpec := externalIPServiceSpecFrom(serviceName, endpointHTTPPort, endpointUDPPort, clusterHTTPPort, clusterUDPPort, endpointsSelector, addresses)
			_, err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Create(context.Background(), externalIPsvcSpec, metav1.CreateOptions{})
			framework.ExpectNoError(err)

			ginkgo.By("Waiting for the endpoints to pop up")
			err = framework.WaitForServiceEndpointsNum(f.ClientSet, f.Namespace.Name, serviceName, len(endPoints), time.Second, wait.ForeverTestTimeout)
			framework.ExpectNoError(err, "failed to validate endpoints for service %s in namespace: %s", serviceName, f.Namespace.Name)

			for _, protocol := range []string{"http", "udp"} {
				for _, externalAddress := range addresses {
					responses := sets.NewString()
					valid := false
					externalPort := int32(clusterHTTPPort)
					if protocol == "udp" {
						externalPort = int32(clusterUDPPort)
					}

					ginkgo.By("Hitting the external service on " + externalAddress + " and reaching all the endpoints " + protocol)
					for i := 0; i < maxTries; i++ {
						epHostname := pokeEndpointHostname(clientContainerName, protocol, externalAddress, externalPort)
						responses.Insert(epHostname)

						// each endpoint returns its hostname. By doing this, we validate that each ep was reached at least once.
						if responses.Equal(nodesHostnames) {
							framework.Logf("Validated external address %s after %d tries", externalAddress, i)
							valid = true
							break
						}
					}
					framework.ExpectEqual(valid, true, "Validation failed for external address", externalAddress)
				}
			}
		})
	})
	ginkgo.Context("Validating ExternalIP ingress traffic to manually added node IPs", func() {
		ginkgo.BeforeEach(func() {
			endPoints = make([]*v1.Pod, 0)
			nodesHostnames = sets.NewString()

			var err error
			nodes, err = e2enode.GetBoundedReadySchedulableNodes(f.ClientSet, 3)
			framework.ExpectNoError(err)

			if len(nodes.Items) < 3 {
				framework.Failf(
					"Test requires >= 3 Ready nodes, but there are only %v nodes",
					len(nodes.Items))
			}

			ginkgo.By("Creating the endpoints pod, one for each worker")
			for _, node := range nodes.Items {
				// this create a udp / http netexec listener which is able to receive the "hostname"
				// command. We use this to validate that each endpoint is received at least once
				args := []string{
					"netexec",
					fmt.Sprintf("--http-port=%d", endpointHTTPPort),
					fmt.Sprintf("--udp-port=%d", endpointUDPPort),
				}
				pod, err := createPod(f, node.Name+"-ep", node.Name, f.Namespace.Name, []string{}, endpointsSelector, func(p *v1.Pod) {
					p.Spec.Containers[0].Args = args
				})
				framework.ExpectNoError(err)
				endPoints = append(endPoints, pod)
				nodesHostnames.Insert(pod.Name)

				// this is arbitrary and mutuated from k8s network e2e tests. We aim to hit all the endpoints at least once
				maxTries = len(endPoints)*len(endPoints) + 30
			}

			ginkgo.By("Creating an external container to send the traffic from")
			// the client uses the netexec command from the agnhost image, which is able to receive commands for poking other
			// addresses.
			createClusterExternalContainer(clientContainerName, agnhostImage, []string{"--network", "kind", "-P"}, []string{"netexec", "--http-port=80"})

			ginkgo.By("Adding ip addresses to each node")
			// add new secondary IP from node subnet to all nodes, if the cluster is v6 add an ipv6 address
			var newIP string
			for i, node := range nodes.Items {
				if utilnet.IsIPv6String(e2enode.GetAddresses(&node, v1.NodeInternalIP)[0]) {
					newIP = "fc00:f853:ccd:e794::" + strconv.Itoa(i)
				} else {
					newIP = "172.18.1." + strconv.Itoa(i)
				}
				// manually add the a secondary IP to each node
				_, err := runCommand("docker", "exec", node.Name, "ip", "addr", "add", newIP, "dev", "breth0")
				if err != nil {
					framework.Failf("failed to add new Addresses to node %s: %v", node.Name, err)
				}

				newNodeAddresses = append(newNodeAddresses, newIP)
			}
		})

		ginkgo.AfterEach(func() {
			deleteClusterExternalContainer(clientContainerName)

			for i, node := range nodes.Items {
				// delete the secondary IP previoulsy added to the nodes
				_, err := runCommand("docker", "exec", node.Name, "ip", "addr", "delete", newNodeAddresses[i], "dev", "breth0")
				if err != nil {
					framework.Failf("failed to delete new Addresses to node %s: %v", node.Name, err)
				}
			}
		})
		// This test validates ingress traffic to externalservices after a new node Ip is added.
		// It creates a service on both udp and tcp and assigns the new node IPs as
		// external Addresses. Then, creates a backend pod on each node.
		// The backend pods are using the agnhost - netexec command which replies to commands
		// with different protocols. We use the "hostname" command to have each backend pod to reply
		// with its hostname.
		// We use an external container to poke the service exposed on the node and we iterate until
		// all the hostnames are returned.
		ginkgo.It("Should be allowed by externalip services to a new node ip", func() {
			serviceName := "externalipsvc"

			ginkgo.By("Creating the externalip service")
			externalIPsvcSpec := externalIPServiceSpecFrom(serviceName, endpointHTTPPort, endpointUDPPort, clusterHTTPPort, clusterUDPPort, endpointsSelector, newNodeAddresses)
			_, err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Create(context.Background(), externalIPsvcSpec, metav1.CreateOptions{})
			framework.ExpectNoError(err)

			ginkgo.By("Waiting for the endpoints to pop up")
			err = framework.WaitForServiceEndpointsNum(f.ClientSet, f.Namespace.Name, serviceName, len(endPoints), time.Second, wait.ForeverTestTimeout)
			framework.ExpectNoError(err, "failed to validate endpoints for service %s in namespace: %s", serviceName, f.Namespace.Name)

			for _, protocol := range []string{"http", "udp"} {
				for _, externalAddress := range newNodeAddresses {
					responses := sets.NewString()
					valid := false
					externalPort := int32(clusterHTTPPort)
					if protocol == "udp" {
						externalPort = int32(clusterUDPPort)
					}

					ginkgo.By("Hitting the external service on " + externalAddress + " and reaching all the endpoints " + protocol)
					for i := 0; i < maxTries; i++ {
						epHostname := pokeEndpointHostname(clientContainerName, protocol, externalAddress, externalPort)
						responses.Insert(epHostname)

						// each endpoint returns its hostname. By doing this, we validate that each ep was reached at least once.
						if responses.Equal(nodesHostnames) {
							framework.Logf("Validated external address %s after %d tries", externalAddress, i)
							valid = true
							break
						}
					}
					framework.ExpectEqual(valid, true, "Validation failed for external address", externalAddress)
				}
			}
		})
	})
})

var _ = ginkgo.Describe("e2e ingress to host-networked pods traffic validation", func() {
	const (
		endpointHTTPPort = 8085
		endpointUDPPort  = 9095
		clusterHTTPPort  = 81
		clusterUDPPort   = 91

		clientContainerName = "npclient"
	)

	f := framework.NewDefaultFramework("nodeport-ingress-test")
	hostNetEndpointsSelector := map[string]string{"hostNetservicebackend": "true"}
	var endPoints []*v1.Pod
	var nodesHostnames sets.String
	maxTries := 0
	var nodes *v1.NodeList
	var externalIpv4 string
	var externalIpv6 string

	// This test validates ingress traffic to nodeports with externalTrafficPolicy Set to local.
	// It creates a nodeport service on both udp and tcp, and creates a host networked
	// backend pod on each node. The backend pod is using the agnhost - netexec command which
	// replies to commands with different protocols. We use the "hostname" and "clientip" commands
	// to have each backend pod to reply with its hostname and the request packet's srcIP.
	// We use an external container to poke the service exposed on the node and ensure that only the
	// nodeport on the node with the backend actually receives traffic and that the packet is not
	// SNATed.
	ginkgo.Context("Validating ingress traffic to Host Netwoked pods", func() {
		ginkgo.BeforeEach(func() {
			endPoints = make([]*v1.Pod, 0)
			nodesHostnames = sets.NewString()

			var err error
			nodes, err = e2enode.GetBoundedReadySchedulableNodes(f.ClientSet, 3)
			framework.ExpectNoError(err)

			if len(nodes.Items) < 3 {
				framework.Failf(
					"Test requires >= 3 Ready nodes, but there are only %v nodes",
					len(nodes.Items))
			}

			ginkgo.By("Creating the endpoints pod, one for each worker")
			for _, node := range nodes.Items {
				// this create a udp / http netexec listener which is able to receive the "hostname"
				// command. We use this to validate that each endpoint is received at least once
				args := []string{
					"netexec",
					fmt.Sprintf("--http-port=%d", endpointHTTPPort),
					fmt.Sprintf("--udp-port=%d", endpointUDPPort),
				}

				// create hostNeworkedPods
				hostNetPod, err := createPod(f, node.Name+"-hostnet-ep", node.Name, f.Namespace.Name, []string{}, hostNetEndpointsSelector, func(p *v1.Pod) {
					p.Spec.Containers[0].Args = args
					p.Spec.HostNetwork = true
				})

				framework.ExpectNoError(err)
				endPoints = append(endPoints, hostNetPod)
				nodesHostnames.Insert(hostNetPod.Name)

				// this is arbitrary and mutuated from k8s network e2e tests. We aim to hit all the endpoints at least once
				maxTries = len(endPoints)*len(endPoints) + 30
			}

			ginkgo.By("Creating an external container to send the traffic from")
			// the client uses the netexec command from the agnhost image, which is able to receive commands for poking other
			// addresses.
			externalIpv4, externalIpv6 = createClusterExternalContainer(clientContainerName, agnhostImage, []string{"--network", "kind", "-P"}, []string{"netexec", "--http-port=80"})
		})

		ginkgo.AfterEach(func() {
			deleteClusterExternalContainer(clientContainerName)
		})
		ginkgo.JustAfterEach(func() {
			// Since we're using host neworked pods in the test wait until namespaces delete after each test
			framework.WaitForNamespacesDeleted(f.ClientSet, []string{f.Namespace.Name}, wait.ForeverTestTimeout)
		})

		// Make sure ingress traffic can reach host pod backends for a service without SNAT when externalTrafficPolicy is set to local
		ginkgo.It("Should be allowed to node local host-networked endpoints by nodeport services with externalTrafficPolicy=local", func() {
			serviceName := "nodeportsvclocalhostnet"
			ginkgo.By("Creating the nodeport service with externalTrafficPolicy=local")
			npSpec := nodePortServiceSpecFrom(serviceName, endpointHTTPPort, endpointUDPPort, clusterHTTPPort, clusterUDPPort, hostNetEndpointsSelector, v1.ServiceExternalTrafficPolicyTypeLocal)
			np, err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Create(context.Background(), npSpec, metav1.CreateOptions{})
			framework.ExpectNoError(err)
			nodeTCPPort, nodeUDPPort := nodePortsFromService(np)

			ginkgo.By("Waiting for the endpoints to pop up")
			err = framework.WaitForServiceEndpointsNum(f.ClientSet, f.Namespace.Name, serviceName, len(endPoints), time.Second, wait.ForeverTestTimeout)
			framework.ExpectNoError(err, "failed to validate endpoints for service %s in namespace: %s", serviceName, f.Namespace.Name)

			for _, protocol := range []string{"http", "udp"} {
				for _, node := range nodes.Items {
					for _, nodeAddress := range node.Status.Addresses {
						// skipping hostnames
						if !addressIsIP(nodeAddress) {
							continue
						}

						responses := sets.NewString()
						// Fill expected responses, it should hit the nodeLocal endpoints and not SNAT packet IP
						expectedResponses := sets.NewString()

						if utilnet.IsIPv6String(nodeAddress.Address) {
							expectedResponses.Insert(node.Name, externalIpv6)
						} else {
							expectedResponses.Insert(node.Name, externalIpv4)
						}

						valid := false
						nodePort := nodeTCPPort
						if protocol == "udp" {
							nodePort = nodeUDPPort
						}

						ginkgo.By("Hitting the nodeport on " + node.Name + " and trying to reach only the local endpoint with protocol " + protocol)
						for i := 0; i < maxTries; i++ {
							epHostname := pokeEndpointHostname(clientContainerName, protocol, nodeAddress.Address, nodePort)
							epClientIP := pokeEndpointClientIP(clientContainerName, protocol, nodeAddress.Address, nodePort)
							responses.Insert(epHostname, epClientIP)

							if responses.Equal(expectedResponses) {
								framework.Logf("Validated local endpoint on node %s with address %s, and packet src IP %s ", node.Name, nodeAddress.Address, epClientIP)
								valid = true
								break
							}

						}
						framework.ExpectEqual(valid, true, "Validation failed for node", node.Name, responses, nodePort)
					}
				}
			}
		})
	})
})

// Send traffic from the cluster node itself to a nodeport service on that node
var _ = ginkgo.Describe("host to host-networked pods traffic validation", func() {
	const (
		endpointHTTPPort = 8085
		endpointUDPPort  = 9095
		clusterHTTPPort  = 81
		clusterUDPPort   = 91
	)

	f := framework.NewDefaultFramework("host-to-host-test")
	hostNetEndpointsSelector := map[string]string{"hostNetservicebackend": "true"}
	var endPoints []*v1.Pod
	var nodesHostnames sets.String
	maxTries := 0
	var nodes *v1.NodeList
	// This test validates ingress traffic to nodeports with externalTrafficPolicy Set to local.
	// It creates a nodeport service on both udp and tcp, and creates a host networked
	// backend pod on each node. The backend pod is using the agnhost - netexec command which
	// replies to commands with different protocols. We use the "hostname" and "clientip" commands
	// to have each backend pod to reply with its hostname and the request packet's srcIP.
	// We use the docker exec command to poke the service exposed on the node from the node itself
	// and ensure that only the nodeport on the node with the backend actually receives traffic and
	// that the packet is not SNATed.
	ginkgo.Context("Validating Host to Host Netwoked pods traffic", func() {
		ginkgo.BeforeEach(func() {
			endPoints = make([]*v1.Pod, 0)
			nodesHostnames = sets.NewString()

			var err error
			nodes, err = e2enode.GetBoundedReadySchedulableNodes(f.ClientSet, 3)
			framework.ExpectNoError(err)

			if len(nodes.Items) < 3 {
				framework.Failf(
					"Test requires >= 3 Ready nodes, but there are only %v nodes",
					len(nodes.Items))
			}

			ginkgo.By("Creating the endpoints pod, one for each worker")
			for _, node := range nodes.Items {
				// this create a http netexec listener which is able to receive the "hostname"
				// command. We use this to validate that each endpoint is received at least once
				args := []string{
					"netexec",
					fmt.Sprintf("--http-port=%d", endpointHTTPPort),
				}

				// create hostNeworkPods
				hostNetPod, err := createPod(f, node.Name+"-hostnet-ep", node.Name, f.Namespace.Name, []string{}, hostNetEndpointsSelector, func(p *v1.Pod) {
					p.Spec.Containers[0].Args = args
					p.Spec.HostNetwork = true
				})

				framework.ExpectNoError(err)
				endPoints = append(endPoints, hostNetPod)
				nodesHostnames.Insert(hostNetPod.Name)

				// this is arbitrary and mutuated from k8s network e2e tests. We aim to hit all the endpoints at least once
				maxTries = len(endPoints)*len(endPoints) + 30
			}
		})
		ginkgo.JustAfterEach(func() {
			// Since we're using host neworked pods in the test wait until namespaces delete after each test
			framework.WaitForNamespacesDeleted(f.ClientSet, []string{f.Namespace.Name}, wait.ForeverTestTimeout)
		})
		// Make sure host sourced traffic can reach host pod backends for a service without SNAT when externalTrafficPolicy is set to local
		// Only verify with http
		ginkgo.It("Should be allowed to node local host-networked endpoints by nodeport services with externalTrafficPolicy=local", func() {
			serviceName := "nodeportsvclocalhostnet"
			ginkgo.By("Creating the nodeport service with externalTrafficPolicy=local")
			npSpec := nodePortServiceSpecFrom(serviceName, endpointHTTPPort, endpointUDPPort, clusterHTTPPort, clusterUDPPort, hostNetEndpointsSelector, v1.ServiceExternalTrafficPolicyTypeLocal)
			np, err := f.ClientSet.CoreV1().Services(f.Namespace.Name).Create(context.Background(), npSpec, metav1.CreateOptions{})
			nodeTCPPort, _ := nodePortsFromService(np)
			framework.ExpectNoError(err)

			ginkgo.By("Waiting for the endpoints to pop up")
			err = framework.WaitForServiceEndpointsNum(f.ClientSet, f.Namespace.Name, serviceName, len(endPoints), time.Second, wait.ForeverTestTimeout)
			framework.ExpectNoError(err, "failed to validate endpoints for service %s in namespace: %s", serviceName, f.Namespace.Name)

			for _, protocol := range []string{"http"} {
				for _, node := range nodes.Items {
					for _, nodeAddress := range node.Status.Addresses {
						// skipping hostnames
						if !addressIsIP(nodeAddress) {
							continue
						}

						responses := sets.NewString()
						// Fill expected responses, it should hit the nodeLocal endpoints only and not SNAT packet IP
						expectedResponses := sets.NewString()

						expectedResponses.Insert(node.Name, nodeAddress.Address)

						valid := false
						nodePort := nodeTCPPort

						ginkgo.By("Hitting the nodeport on " + node.Name + " and trying to reach only the local endpoint with protocol " + protocol)
						for i := 0; i < maxTries; i++ {
							epHostname := curlInContainer(node.Name, protocol, nodeAddress.Address, nodePort, "hostname")
							epClientIP := curlInContainer(node.Name, protocol, nodeAddress.Address, nodePort, "clientip")
							ip, _, err := net.SplitHostPort(epClientIP)
							framework.ExpectNoError(err)
							responses.Insert(epHostname, ip)

							if responses.Equal(expectedResponses) {
								framework.Logf("Validated local endpoint on node %s with address %s, and packet src IP %s ", node.Name, nodeAddress.Address, epClientIP)
								valid = true
								break
							}

						}
						framework.ExpectEqual(valid, true, "Validation failed for node", node.Name, responses, nodePort)
					}
				}
			}
		})
	})
})

// This test validates ingress traffic sourced from a mock external gateway
// running as a container. Add a namespace annotated with the IP of the
// mock external container's eth0 address. Add a loopback address and a
// route pointing to the pod in the test namespace. Validate connectivity
// sourcing from the mock gateway container loopback to the test ns pod.
var _ = ginkgo.Describe("e2e ingress gateway traffic validation", func() {
	const (
		svcname     string = "novxlan-externalgw-ingress"
		gwContainer string = "gw-ingress-test-container"
	)

	f := framework.NewDefaultFramework(svcname)

	type nodeInfo struct {
		name   string
		nodeIP string
	}

	var (
		workerNodeInfo nodeInfo
	)

	ginkgo.BeforeEach(func() {

		// retrieve worker node names
		nodes, err := e2enode.GetBoundedReadySchedulableNodes(f.ClientSet, 3)
		framework.ExpectNoError(err)
		if len(nodes.Items) < 3 {
			framework.Failf(
				"Test requires >= 3 Ready nodes, but there are only %v nodes",
				len(nodes.Items))
		}
		ips := e2enode.CollectAddresses(nodes, v1.NodeInternalIP)
		workerNodeInfo = nodeInfo{
			name:   nodes.Items[1].Name,
			nodeIP: ips[1],
		}
	})

	ginkgo.AfterEach(func() {
		// tear down the container simulating the gateway
		if cid, _ := runCommand("docker", "ps", "-qaf", fmt.Sprintf("name=%s", gwContainer)); cid != "" {
			if _, err := runCommand("docker", "rm", "-f", gwContainer); err != nil {
				framework.Logf("failed to delete the gateway test container %s %v", gwContainer, err)
			}
		}
	})

	ginkgo.It("Should validate ingress connectivity from an external gateway", func() {

		var (
			pingDstPod     string
			dstPingPodName = "e2e-exgw-ingress-ping-pod"
			command        = []string{"bash", "-c", "sleep 20000"}
			exGWLo         = "10.30.1.1"
			exGWLoCidr     = fmt.Sprintf("%s/32", exGWLo)
			pingCount      = "3"
		)

		// start the first container that will act as an external gateway
		_, err := runCommand("docker", "run", "-itd", "--privileged", "--network", externalContainerNetwork, "--name", gwContainer, "centos/tools")
		if err != nil {
			framework.Failf("failed to start external gateway test container %s: %v", gwContainer, err)
		}
		exGWIp, _ := getContainerAddressesForNetwork(gwContainer, externalContainerNetwork)

		// annotate the test namespace with the external gateway address
		annotateArgs := []string{
			"annotate",
			"namespace",
			f.Namespace.Name,
			fmt.Sprintf("k8s.ovn.org/routing-external-gws=%s", exGWIp),
		}
		framework.Logf("Annotating the external gateway test namespace to container gateway: %s", exGWIp)
		framework.RunKubectlOrDie(f.Namespace.Name, annotateArgs...)

		nodeIP, _ := getContainerAddressesForNetwork(workerNodeInfo.name, externalContainerNetwork)
		framework.Logf("the pod side node is %s and the source node ip is %s", workerNodeInfo.name, nodeIP)
		podCIDR, err := getNodePodCIDR(workerNodeInfo.name)
		if err != nil {
			framework.Failf("Error retrieving the pod cidr from %s %v", workerNodeInfo.name, err)
		}
		framework.Logf("the pod cidr for node %s is %s", workerNodeInfo.name, podCIDR)

		// Create the pod that will be used as the source for the connectivity test
		createGenericPod(f, dstPingPodName, workerNodeInfo.name, f.Namespace.Name, command)
		// wait for the pod setup to return a valid address
		err = wait.PollImmediate(retryInterval, retryTimeout, func() (bool, error) {
			pingDstPod = getPodAddress(dstPingPodName, f.Namespace.Name)
			validIP := net.ParseIP(pingDstPod)
			if validIP == nil {
				return false, nil
			}
			return true, nil
		})
		// fail the test if a pod address is never retrieved
		if err != nil {
			framework.Failf("Error trying to get the pod IP address")
		}
		// add a host route on the gateways for return traffic to the pod
		_, err = runCommand("docker", "exec", gwContainer, "ip", "route", "add", pingDstPod, "via", nodeIP)
		if err != nil {
			framework.Failf("failed to add the pod host route on the test container %s: %v", gwContainer, err)
		}
		// add a loopback address to the mock container that will source the ingress test
		_, err = runCommand("docker", "exec", gwContainer, "ip", "address", "add", exGWLoCidr, "dev", "lo")
		if err != nil {
			framework.Failf("failed to add the loopback ip to dev lo on the test container: %v", err)
		}

		// Validate connectivity from the external gateway loopback to the pod in the test namespace
		ginkgo.By(fmt.Sprintf("Validate ingress traffic from the external gateway %s can reach the pod in the exgw annotated namespace", gwContainer))
		// generate traffic that will verify connectivity from the mock external gateway loopback
		_, err = runCommand("docker", "exec", gwContainer, "ping", "-c", pingCount, "-S", exGWLo, "-I", "eth0", pingDstPod)
		if err != nil {
			framework.Failf("failed to ping the pod address %s from mock container %s: %v", pingDstPod, gwContainer, err)
		}
	})
})