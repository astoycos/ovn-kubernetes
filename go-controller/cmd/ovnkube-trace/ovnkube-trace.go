
package main

import (
	"bytes"
	"context"
	"encoding/json"
	//"errors"
	"io"
	"flag"
	"fmt"
	"github.com/sirupsen/logrus"
	"os"
	//"os/exec"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"

	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kapi "k8s.io/api/core/v1"
)

var (
	logLevels = []string{"debug", "info", "warn", "error", "fatal", "panic"}
	logLevel  = "fatal"
)

const (
	ovnNamespace = "openshift-ovn-kubernetes"
)

type AddrInfo struct {
	Family    string `json:"family,omitempty"`
	Local     string `json:"local,omitempty"`
	Prefixlen int    `json:"prefixlen,omitempty"`
}
type IpAddrReq struct {
	IfIndex   int        `json:"ifindex,omitempty"`
	IfName    string     `json:"ifname,omitempty"`
	LinkIndex int        `json:"link_index,omitempty"`
	AInfo     []AddrInfo `json:"addr_info,omitempty"`
}

type PodInfo struct {
	IP                      string
	MAC                     string
	VethName                string
	OVNName                 string
	PodName                 string
	ContainerName           string
	OvnKubeContainerPodName string
	NodeName                string
	StorPort                string
	StorMAC                 string
	HostNetwork             bool
}

func execInPod(coreclient *corev1client.CoreV1Client, restconfig *rest.Config, namespace string, podName string, containerName string, cmd string, in string) (string, string, error) {

	scheme := runtime.NewScheme()
	if err := kapi.AddToScheme(scheme); err != nil {
		fmt.Errorf("error adding to scheme: %v", err)
		os.Exit(-1)
	}
	parameterCodec := runtime.NewParameterCodec(scheme)

	useStdin := false
	if in != "" {
	   useStdin = true
	}

	// Prepare the API URL used to execute another process within the Pod.

	req := coreclient.RESTClient().
		Post().
		Namespace(namespace).
		Resource("pods").
		Name(podName).
		SubResource("exec").
		VersionedParams(&kapi.PodExecOptions{
			Container: containerName,
			Command:   []string{"bash", "-c", cmd},
			Stdin:     useStdin,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, parameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(restconfig, "POST", req.URL())
	if err != nil {
		panic(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var stdin  io.Reader

	if useStdin {
	   stdin = strings.NewReader(in)
	} else {
	   stdin = nil
	}

	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return stdout.String(), stderr.String(), err
	}

	return stdout.String(), stderr.String(), err
}

func getPodMAC(client *corev1client.CoreV1Client, pod *kapi.Pod) (podMAC string) {

	if pod.Spec.HostNetwork {
		node, err := client.Nodes().Get(context.TODO(), pod.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			panic(err)
		}

		nodeMAC, err := util.ParseNodeManagementPortMACAddress(node)
		if err != nil {
			panic(err)
		}
		if nodeMAC != nil {
			podMAC = nodeMAC.String()
		}
	} else {
		podAnnotation, err := util.UnmarshalPodAnnotation(pod.ObjectMeta.Annotations)
		if err != nil {
			panic(err)
		}
		if podAnnotation != nil {
			podMAC = podAnnotation.MAC.String()
		}
	}

	return podMAC
}

func getPodInfo(coreclient *corev1client.CoreV1Client, restconfig *rest.Config, podName string, namespace string, cmd string) (podInfo *PodInfo, err error) {

	var ethName string

	// Get pod with the name supplied by srcPodName
	pod, err := coreclient.Pods(namespace).Get(context.TODO(), podName, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Pod %s in namespace %s not found\n", podName, namespace)
		return nil, err
	}

	podInfo = &PodInfo{
		IP: pod.Status.PodIP,
	}

	// Get the node on which the src pod runs on
	node, err := coreclient.Nodes().Get(context.TODO(), pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		logrus.Errorf("Pod %s in namespace %s node not found\n", podName, namespace)
		return nil, err
	}
	logrus.Debugf("==>Got pod %s which is running on node %s\n", podName, node.Name)

	podMAC := getPodMAC(coreclient, pod)

	podInfo.PodName = pod.Name
	podInfo.MAC = podMAC
	podInfo.ContainerName = pod.Spec.Containers[0].Name

	var tryJSON bool = true
	var linkIndex int

	// The interface name used depends on what network namespasce the pod uses
	if pod.Spec.HostNetwork {
		ethName = util.K8sMgmtIntfName
		podInfo.HostNetwork = true
		linkIndex = 0
	} else {
		ethName = "eth0"
		podInfo.HostNetwork = false

		// Find index used for pod interface

		ipJaddrCmd := "ip -j addr show " + ethName

		logrus.Debugf("ip -j command is %s", ipJaddrCmd)
		ipOutput, ipError, err := execInPod(coreclient, restconfig, namespace, podName, podInfo.ContainerName, ipJaddrCmd, "")
		if err != nil {
			logrus.Errorf("ip -j command error %v stdOut: %s\n stdErr: %s", err, ipOutput, ipError)
			tryJSON = false
		}

		if tryJSON {
			//logrus.Debugf("==>pod %s: ip addr show: %q", pod.Name, outPod.String())
			logrus.Debugf("==>pod %s: ip addr show: %q", pod.Name, ipOutput)

			var data []IpAddrReq
			//ipOutput = strings.Replace(outPod.String(), "\n", "", -1)
			ipOutput = strings.Replace(ipOutput, "\n", "", -1)
			logrus.Debugf("==> pod %s NOW: %s ", pod.Name, ipOutput)
			err = json.Unmarshal([]byte(ipOutput), &data)
			if err != nil {
				fmt.Printf("JSON ERR: couldn't get stuff from data %v; json parse error: %v\n", data, err)
				panic(err)
			}
			logrus.Debugf("size of IpAddrReq array: %v\n", len(data))
			logrus.Debugf("IpAddrReq: %v\n", data)

			for _, addr := range data {
				if addr.IfName == ethName {
					linkIndex = addr.LinkIndex
					logrus.Debugf("ifName: %v", addr.IfName)
					break
				}
			}
			logrus.Debugf("linkIndex is %d", linkIndex)
		} else {
			awkString := " | awk '{print $2}'"
			iplinkCmd := "ip -o link show dev " + ethName + awkString

			logrus.Debugf("ip -o command is %s", iplinkCmd)
			linkOutput, linkError, err := execInPod(coreclient, restconfig, namespace, podName, podInfo.ContainerName, iplinkCmd, "")
			if err != nil {
				logrus.Errorf("ip -o command error %v stdOut: %s\n stdErr: %s", err, linkOutput, linkError)
				// Give up, pod image doesn't have iproute installed
				return nil, err
			}

			logrus.Debugf("AWK string is %s", awkString)
			logrus.Debugf("==>pod Old Way %s: ip -o link show: %q", pod.Name, linkOutput)

			linkOutput = strings.Replace(linkOutput, "\n", "", -1)
			logrus.Debugf("==> pod Old Way %s NOW: %s ", pod.Name, linkOutput)
			linkOutput = strings.Replace(linkOutput, "eth0@if", "", -1)
			logrus.Debugf("==> pod Old Way %s NOW: %s ", pod.Name, linkOutput)
			linkOutput = strings.Replace(linkOutput, ":", "", -1)
			logrus.Debugf("==> pod Old Way %s NOW: %s ", pod.Name, linkOutput)

			linkIndex, err = strconv.Atoi(linkOutput)
			if err != nil {
				logrus.Error("Error converting string to int", err)
				return nil, err
			}
			logrus.Debugf("Using ip -o link show - linkIndex is %d", linkIndex)
		}
	}
	logrus.Debugf("Using interface name of %s with MAC of %s", ethName, podMAC)

	if !pod.Spec.HostNetwork && linkIndex == 0 {
		logrus.Fatalf("Fatal: Pod Network used and linkIndex is zero")
		return nil, err
	}
	if pod.Spec.HostNetwork && linkIndex != 0 {
		logrus.Errorf("Fatal: Host Network used and linkIndex is non-zero")
		return nil, err
	}

	// Get pods in the openshift-ovn-kubernetes namespace
	podsOvn, errOvn := coreclient.Pods(ovnNamespace).List(context.TODO(), metav1.ListOptions{})
	if errOvn != nil {
		logrus.Panicf("Cannot find pods in %s namespace", ovnNamespace)
		return nil, errOvn
	}

	var ovnkubePod *kapi.Pod
	// Find ovnkube-node-xxx pod running on the same node as srcPod
	for _, podOvn := range podsOvn.Items {
		if podOvn.Spec.NodeName == node.Name {
			if !strings.HasPrefix(podOvn.Name, "ovnkube-node-metrics") {
				if strings.HasPrefix(podOvn.Name, "ovnkube-node") {
					logrus.Debugf("==> pod %s is running on node %s", podOvn.Name, node.Name)
					ovnkubePod = &podOvn
					break
				}
			}
		}
	}
	if ovnkubePod == nil {
		panic(err)
	}

	podInfo.OvnKubeContainerPodName = ovnkubePod.Name
	podInfo.NodeName = ovnkubePod.Spec.NodeName
	podInfo.StorPort = "stor-" + ovnkubePod.Spec.NodeName

	//
	// ovn-nbctl  -p /ovn-cert/tls.key -c /ovn-cert/tls.crt -C /ovn-ca/ca-bundle.crt  --db ssl:10.0.0.6:9641,ssl:10.0.0.8:9641,ssl:10.0.0.7:9641 lsp-get-addresses stor-qe-anurag54-hmprt-master-0

	// Find stor MAC

	logrus.Debugf("command is: %s", "ovn-nbctl "+cmd+" lsp-get-addresses "+"stor-"+ovnkubePod.Spec.NodeName)
	lspCmd := "ovn-nbctl " + cmd + " lsp-get-addresses " + "stor-" + ovnkubePod.Spec.NodeName
	ipOutput, ipError, err := execInPod(coreclient, restconfig, ovnNamespace, ovnkubePod.Name, "ovnkube-node", lspCmd, "")
	if err != nil {
		fmt.Printf("execInPod() failed with %s stderr %s stdout %s \n", err, ipError, ipOutput)
		logrus.Debugf("execInPod() failed err %s - podInfo %v - ovnkubePod Name %s", err, podInfo, ovnkubePod.Name)
		return nil, err
	}

	podInfo.StorMAC = strings.Replace(ipOutput, "\n", "", -1)

	if ethName == util.K8sMgmtIntfName {
		podInfo.OVNName = util.K8sPrefix + ovnkubePod.Spec.NodeName
		podInfo.VethName = util.K8sMgmtIntfName
		logrus.Debugf("hostInterface on host stack OVN name is %s\n", podInfo.OVNName)
	} else {

		// obnkube-node-xxx uses host network.  Find host end of veth matching pod eth0 index

		tryJSON = true
		var hostInterface string

		ipCmd := "ip -j addr show"
		logrus.Debugf("command is: %s", ipCmd)

		hostOutput, hostError, err := execInPod(coreclient, restconfig, ovnNamespace, ovnkubePod.Name, "ovnkube-node", ipCmd, "")
		if err != nil {
			fmt.Printf("execInPod() failed with %s stderr %s stdout %s \n", err, hostError, hostOutput)
			logrus.Debugf("execInPod() failed err %s - podInfo %v - ovnkubePod Name %s", err, podInfo, ovnkubePod.Name)
			return nil, err
		}

		if tryJSON {
			logrus.Debugf("==>ovnkubePod %s: ip addr show: %q", ovnkubePod.Name, hostOutput)

			var data []IpAddrReq
			hostOutput = strings.Replace(hostOutput, "\n", "", -1)
			logrus.Debugf("==> host %s NOW: %s", ovnkubePod.Name, hostOutput)
			err = json.Unmarshal([]byte(hostOutput), &data)
			if err != nil {
				logrus.Errorf("JSON ERR: couldn't get stuff from data %v; json parse error: %v", data, err)
				return nil, err
			}
			logrus.Debugf("size of IpAddrReq array: %v", len(data))
			logrus.Debugf("IpAddrReq: %v", data)

			for _, addr := range data {
				if addr.IfIndex == linkIndex {
					hostInterface = addr.IfName
					logrus.Debugf("ifName: %v\n", addr.IfName)
					break
				}
			}
			logrus.Debugf("hostInterface is %s", hostInterface)
		} else {

			ipoCmd := "ip -o addr show"
			logrus.Debugf("command is: %s", ipoCmd)

			hostOutput, hostError, err := execInPod(coreclient, restconfig, ovnNamespace, ovnkubePod.Name, "ovnkube-node", ipoCmd, "")
			if err != nil {
				fmt.Printf("execInPod() failed with %s stderr %s stdout %s \n", err, hostError, hostOutput)
				logrus.Debugf("execInPod() failed err %s - podInfo %v - ovnkubePod Name %s", err, podInfo, ovnkubePod.Name)
				return nil, err
			}

			hostOutput = strings.Replace(hostOutput, "\n", "", -1)
			logrus.Debugf("==>node %s: ip addr show: %q", node.Name, hostOutput)

			idx := strconv.Itoa(linkIndex) + ": "
			result := strings.Split(hostOutput, idx)
			logrus.Debugf("result[0]: %s", result[0])
			logrus.Debugf("result[1]: %s", result[1])
			words := strings.Fields(result[1])
			for i, word := range words {
				if i == 0 {
					hostInterface = word
					break
				}
			}
		}
		logrus.Debugf("hostInterface name is %s\n", hostInterface)
		podInfo.VethName = hostInterface
	}

	return podInfo, err
}

func setupLogging() {
	var found bool
	for _, l := range logLevels {
		if l == strings.ToLower(logLevel) {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "Log Level %q is not supported, choose from: %s\n", logLevel, strings.Join(logLevels, ", "))
		os.Exit(1)
	}

	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		fmt.Fprint(os.Stderr, err.Error())
		os.Exit(1)
	}
	logrus.SetLevel(level)

	if logrus.IsLevelEnabled(logrus.InfoLevel) {
		logrus.Infof("%s filtering at log level %s", os.Args[0], logrus.GetLevel())
	}
}

func main() {
	var psrcNamespace *string
	var pdstNamespace *string
	var protocol string

	psrcNamespace = flag.String("src-namespace", "default", "k8s namespace of source pod")
	pdstNamespace = flag.String("dst-namespace", "default", "k8s namespace of dest pod")

	cliConfig := flag.String("kubeconfig", "", "absolute path to the kubeconfig file")

	srcPodName := flag.String("src", "", "src: source pod name")
	dstPodName := flag.String("dst", "", "dest: destination pod name")
	dstSvcName := flag.String("service", "", "service: destination service name")
	dstPort := flag.String("dst-port", "80", "dst-port: destination port")
	tcp := flag.Bool("tcp", false, "use tcp transport protocol")
	udp := flag.Bool("udp", false, "use udp transport protocol")

	loglevel := flag.String("log-level", "error", "log-level")

	flag.Parse()
	srcNamespace := *psrcNamespace
	dstNamespace := *pdstNamespace
	logLevel = *loglevel

	setupLogging()

	logrus.Debugf("log level is set to Debug")

	if *srcPodName == "" {
		fmt.Printf("Usage: source pod must be specified\n")
		logrus.Errorf("Usage: source pod must be specified")
		os.Exit(-1)
	}
	if !*tcp && !*udp {
		fmt.Printf("Usage: either tcp or udp must be specified\n")
		logrus.Errorf("Usage: either tcp or udp must be specified")
		os.Exit(-1)
	}
	if *udp && *tcp {
		fmt.Printf("Usage: Both tcp or udp cannot be specified\n")
		logrus.Errorf("Usage: Both tcp or udp cannot be specified")
		os.Exit(-1)
	}
	if *tcp {
		if *dstSvcName == "" && *dstPodName == "" {
			fmt.Printf("Usage: destination pod or destination service must be specified for tcp\n")
			logrus.Errorf("Usage: destination pod or destination service must be specified for tcp")
			os.Exit(-1)
		} else {
			protocol = "tcp"
		}
	}
	if *udp {
		if *dstPodName == "" {
			fmt.Printf("Usage: destination pod must be specified for udp\n")
			logrus.Errorf("Usage: destination pod must be specified for udp")
			os.Exit(-1)
		} else {
			protocol = "udp"
		}
	}

	var restconfig *rest.Config
	var err error

	// This might work better?  https://godoc.org/sigs.k8s.io/controller-runtime/pkg/client/config

	// When supplied the kubeconfig supplied via cli takes precedence
	if *cliConfig != "" {

		// use the current context in kubeconfig
		restconfig, err = clientcmd.BuildConfigFromFlags("", *cliConfig)
		if err != nil {
			logrus.Errorf(" Unexpected error: %v", err)
			os.Exit(-1)
		}
		//kubeconfig := os.Getenv("KUBECONFIG")
		//fmt.Printf("**cli: kubeconfig: type %T value: %s\n", kubeconfig, kubeconfig)
	} else {

		// Instantiate loader for kubeconfig file.
		kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		)

		// Get a rest.Config from the kubeconfig file.  This will be passed into all
		// the client objects we create.
		restconfig, err = kubeconfig.ClientConfig()
		if err != nil {
			logrus.Errorf(" Unexpected error: %v", err)
			os.Exit(-1)
		}
	}

	// Create a Kubernetes core/v1 client.
	coreclient, err := corev1client.NewForConfig(restconfig)
	if err != nil {
		logrus.Errorf(" Unexpected error: %v", err)
		os.Exit(-1)
	}

	// List all Nodes.
	nodes, err := coreclient.Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		logrus.Errorf(" Unexpected error: %v", err)
		os.Exit(-1)
	}

	masters := make(map[string]string)
	workers := make(map[string]string)

	logrus.Debugf(" Nodes: ")
	for _, node := range nodes.Items {

		_, found := node.Labels["node-role.kubernetes.io/master"]
		if found {
			logrus.Debugf("  Name: %s is a master", node.Name)
			for _, s := range node.Status.Addresses {
				logrus.Debugf("  Address Type: %s - Address: %s", s.Type, s.Address)
				//if s.Type == corev1client.NodeInternalIP {
				if s.Type == "InternalIP" {
					masters[node.Name] = s.Address
				}
			}
		} else {
			logrus.Debugf("  Name: %s is a worker", node.Name)
			for _, s := range node.Status.Addresses {
				logrus.Debugf("  Address Type: %s - Address: %s", s.Type, s.Address)
				//if s.Type == corev1client.NodeInternalIP {
				if s.Type == "InternalIP" {
					workers[node.Name] = s.Address
				}
			}
		}
	}

	if len(masters) < 3 {
		logrus.Infof("Cluster does not have 3 masters, found %d", len(masters))
		//os.Exit(-1)
	}

	// Common ssl parameters
	sslCertKeys := "-p /ovn-cert/tls.key -c /ovn-cert/tls.crt -C /ovn-ca/ca-bundle.crt "

	//nbcmd := "-p /ovn-cert/tls.key -c /ovn-cert/tls.crt -C /ovn-ca/ca-bundle.crt --db "
	nbUri := " "
	nbCount := 0
	for k, v := range masters {
		logrus.Debugf("master name: %s has IP %s", k, v)
		nbUri += "ssl:" + v + ":9641"
		if nbCount == 2 {
			nbUri += " "
		} else {
			nbUri += ","
		}
		nbCount++
	}
	nbcmd := sslCertKeys + "--db " + nbUri
	logrus.Debugf("nbcmd is %s", nbcmd)

	sbUri := " "
	sbCount := 0
	for _, v := range masters {
		sbUri += "ssl:" + v + ":9642"
		if sbCount == 2 {
			sbUri += " "
		} else {
			sbUri += ","
		}
		sbCount++
	}
	sbcmd := sslCertKeys + "--db " + sbUri
	logrus.Debugf("sbcmd is %s", sbcmd)

	// Get info needed for the src Pod
	srcPodInfo, err := getPodInfo(coreclient, restconfig, *srcPodName, srcNamespace, nbcmd)
	if err != nil {
		fmt.Printf("Failed to get information from pod %s: %v\n", *srcPodName, err)
		logrus.Errorf("Failed to get information from pod %s: %v", *srcPodName, err)
		os.Exit(-1)
	}
	logrus.Debugf("srcPodInfo is %v", srcPodInfo)

	// Now get info needed for the dst Pod
	dstPodInfo, err := getPodInfo(coreclient, restconfig, *dstPodName, dstNamespace, nbcmd)
	if err != nil {
		fmt.Printf("Failed to get information from pod %s: %v\n", *dstPodName, err)
		logrus.Errorf("Failed to get information from pod %s: %v", *dstPodName, err)
		os.Exit(-1)
	}
	logrus.Debugf("dstPodInfo is %v\n", dstPodInfo)

	// At least one pod must not be on the Host Network
	if srcPodInfo.HostNetwork && dstPodInfo.HostNetwork {
		fmt.Printf("Both pods cannot be on Host Network; use ping\n")
		os.Exit(-1)
	}

	// ovn-trace from src pod to dst pod

	var fromSrc string

	//fromSrc := " 'inport==\"" + namespace + "_" + *srcPodName + "\""
	if srcPodInfo.HostNetwork {
		fromSrc = " 'inport==\"" + srcPodInfo.OVNName + "\""
	} else {
		fromSrc = " 'inport==\"" + srcNamespace + "_" + *srcPodName + "\""
	}
	fromSrc += " && eth.dst==" + srcPodInfo.StorMAC
	fromSrc += " && eth.src==" + srcPodInfo.MAC
	fromSrc += " && ip4.dst==" + dstPodInfo.IP
	fromSrc += " && ip4.src==" + srcPodInfo.IP
	fromSrc += " && ip.ttl==64"
	fromSrc += " && " + protocol + ".dst==" + *dstPort + " && " + protocol + ".src==52888'"

	fromSrcCmd := "ovn-trace " + sbcmd + " " + srcPodInfo.NodeName + " " + fromSrc

	logrus.Debugf("ovn-trace command from src to dst is %s", fromSrcCmd)
	ovnSrcDstOut, ovnSrcDstErr, err := execInPod(coreclient, restconfig, ovnNamespace, srcPodInfo.OvnKubeContainerPodName, "ovnkube-node", fromSrcCmd, "")
	if err != nil {
		logrus.Errorf("Source to Destination ovn-trace error %v stdOut: %s\n stdErr: %s", err, ovnSrcDstOut, ovnSrcDstErr)
		os.Exit(-1)
	}
	fmt.Printf("Source to Destination ovn-trace Output: %s\n", ovnSrcDstOut)

	// Trace from dst pod to src pod

	var fromDst string

	//fromDst := " 'inport==\"" + namespace + "_" + *dstPodName + "\""
	if dstPodInfo.HostNetwork {
		fromDst = " 'inport==\"" + dstPodInfo.OVNName + "\""
	} else {
		fromDst = " 'inport==\"" + dstNamespace + "_" + *dstPodName + "\""
	}
	fromDst += " && eth.dst==" + dstPodInfo.StorMAC
	fromDst += " && eth.src==" + dstPodInfo.MAC
	fromDst += " && ip4.dst==" + srcPodInfo.IP
	fromDst += " && ip4.src==" + dstPodInfo.IP
	fromDst += " && ip.ttl==64"
	fromDst += " && " + protocol + ".src==" + *dstPort + " && " + protocol + ".dst==52888'"

	fromDstCmd := "ovn-trace " + sbcmd + " " + dstPodInfo.NodeName + " " + fromDst

	logrus.Debugf("ovn-trace command from dst to src is %s", fromDstCmd)
	ovnDstSrcOut, ovnDstSrcErr, err := execInPod(coreclient, restconfig, ovnNamespace, srcPodInfo.OvnKubeContainerPodName, "ovnkube-node", fromDstCmd, "")
	if err != nil {
		logrus.Errorf("Source to Destination ovn-trace error %v stdOut: %s\n stdErr: %s", err, ovnDstSrcOut, ovnDstSrcErr)
		os.Exit(-1)
	}
	fmt.Printf("Destination to Source ovn-trace Output: %s\n", ovnDstSrcOut)

	// ovs-appctl ofproto/trace: src pod to dst pod

	fromSrc = "ofproto/trace br-int"
	fromSrc += " \"in_port=" + srcPodInfo.VethName + ", " + protocol + ","
	fromSrc += " dl_dst=" + srcPodInfo.StorMAC + ","
	fromSrc += " dl_src=" + srcPodInfo.MAC + ","
	fromSrc += " nw_dst=" + dstPodInfo.IP + ","
	fromSrc += " nw_src=" + srcPodInfo.IP + ","
	fromSrc += " nw_ttl=64" + ","
	fromSrc += " " + protocol + "_dst=" + *dstPort + ","
	fromSrc += " " + protocol + "_src=" + "12345\""

	fromSrcCmd = "ovs-appctl " + fromSrc

	logrus.Debugf("ovs-appctl ofproto/trace command from src to dst is %s", fromSrc)
	appSrcDstOut, appSrcDstErr, err := execInPod(coreclient, restconfig, ovnNamespace, srcPodInfo.OvnKubeContainerPodName, "ovnkube-node", fromSrcCmd, "")
	if err != nil {
		logrus.Errorf("Source to Destination ovs-appctl error %v stdOut: %s\n stdErr: %s", err, appSrcDstOut, appSrcDstErr)
		os.Exit(-1)
	}
	fmt.Printf("Source to Destination ovs-appctl Output: %s\n", appSrcDstOut)

	// ovs-appctl ofproto/trace: dst pod to src pod

	fromDst = "ofproto/trace br-int"
	fromDst += " \"in_port=" + dstPodInfo.VethName + ", " + protocol + ","
	fromDst += " dl_dst=" + dstPodInfo.StorMAC + ","
	fromDst += " dl_src=" + dstPodInfo.MAC + ","
	fromDst += " nw_dst=" + srcPodInfo.IP + ","
	fromDst += " nw_src=" + dstPodInfo.IP + ","
	fromDst += " nw_ttl=64" + ","
	fromDst += " " + protocol + "_src=" + *dstPort + ","
	fromDst += " " + protocol + "_dst=" + "12345\""

	fromDstCmd = "ovs-appctl " + fromDst

	logrus.Debugf("ovs-appctl ofproto/trace command from dst to src is %s", fromDst)
	appDstSrcOut, appDstSrcErr, err := execInPod(coreclient, restconfig, ovnNamespace, dstPodInfo.OvnKubeContainerPodName, "ovnkube-node", fromDstCmd, "")
	if err != nil {
		logrus.Errorf("Destination to Source ovs-appctl error %v stdOut: %s\n stdErr: %s", err, appDstSrcOut, appDstSrcErr)
		os.Exit(-1)
	}
	fmt.Printf("Destination to Source ovs-appctl Output: %s\n", appDstSrcOut)

	// ovn-detrace src - dst

	fromSrc  = "--ovnsb=" + sbUri + " "
	fromSrc += "--ovnnb=" + nbUri + " "
	fromSrc += sslCertKeys + " "
	fromSrc += "--ovs --ovsdb=unix:/var/run/openvswitch/db.sock "

	fromSrcCmd = "ovn-detrace " + fromSrc

	logrus.Debugf("ovn-detrace command from src to dst is %s", fromSrc)
	dtraceSrcDstOut, dtraceSrcDstErr, err := execInPod(coreclient, restconfig, ovnNamespace, srcPodInfo.OvnKubeContainerPodName, "ovnkube-node", fromSrcCmd, ovnSrcDstOut)
	if err != nil {
		logrus.Errorf("Source to Destination ovn-detrace error %v stdOut: %s\n stdErr: %s", err, dtraceSrcDstOut, dtraceSrcDstErr)
		os.Exit(-1)
	}
	fmt.Printf("Source to Destination ovn-detrace Output: %s\n", dtraceSrcDstOut)

	fromDst  = "--ovnsb=" + sbUri + " "
	fromDst += "--ovnnb=" + nbUri + " "
	fromDst += sslCertKeys + " "
	fromDst += "--ovs --ovsdb=unix:/var/run/openvswitch/db.sock " 

	fromDstCmd = "ovn-detrace " + fromDst

	logrus.Debugf("ovn-detrace command from dst to src is %s", fromDst)
	dtraceDstSrcOut, dtraceDstSrcErr, err := execInPod(coreclient, restconfig, ovnNamespace, dstPodInfo.OvnKubeContainerPodName, "ovnkube-node", fromDstCmd, ovnDstSrcOut)
	if err != nil {
		logrus.Errorf("Destination to Source ovn-detrace error %v stdOut: %s\n stdErr: %s", err, dtraceDstSrcOut, dtraceDstSrcErr)
		os.Exit(-1)
	}
	fmt.Printf("Destination to Source detrace Output: %s\n", appDstSrcOut)

	// TODO Next
	//
	fmt.Println("ovn-trace command Completed normally")
}
