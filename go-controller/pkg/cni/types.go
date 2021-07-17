package cni

import (
	"context"
	"net/http"
	"time"

	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

// serverRunDir is the default directory for CNIServer runtime files
const serverRunDir string = "/var/run/ovn-kubernetes/cni/"

const serverSocketName string = "ovn-cni-server.sock"
const serverSocketPath string = serverRunDir + "/" + serverSocketName

// KubeAPIAuth contains information necessary to create a Kube API client
type KubeAPIAuth struct {
	// Kubeconfig is the path to a kubeconfig
	Kubeconfig string `json:"kubeconfig,omitempty"`
	// KubeAPIServer is the URL of a Kubernetes API server (not required if kubeconfig is given)
	KubeAPIServer string `json:"kube-api-server,omitempty"`
	// KubeAPIToken is a Kubernetes API token (not required if kubeconfig is given)
	KubeAPIToken string `json:"kube-api-token,omitempty"`
	// KubeCAData is the Base64-ed Kubernetes API CA certificate data (not required if kubeconfig is given)
	KubeCAData string `json:"kube-ca-data,omitempty"`
}

// PodInterfaceInfo consists of interface info result from cni server if cni client configure's interface
type PodInterfaceInfo struct {
	util.PodAnnotation

	MTU         int   `json:"mtu"`
	Ingress     int64 `json:"ingress"`
	CheckExtIDs bool  `json:"check-external-ids"`
	Egress      int64 `json:"egress"`
}

// Explicit type for CNI commands the server handles
type command string

// CNIAdd is the command representing add operation for a new pod
const CNIAdd command = "ADD"

// CNIUpdate is the command representing update operation for an existing pod
const CNIUpdate command = "UPDATE"

// CNIDel is the command representing delete operation on a pod that is to be torn down
const CNIDel command = "DEL"

// CNICheck is the command representing check operation on a pod
const CNICheck command = "CHECK"

// Request sent to the Server by the OVN CNI plugin
type Request struct {
	// CNI environment variables, like CNI_COMMAND and CNI_NETNS
	Env map[string]string `json:"env,omitempty"`
	// CNI configuration passed via stdin to the CNI plugin
	Config []byte `json:"config,omitempty"`
}

// CNIRequestMetrics info to report from CNI shim to CNI server
type CNIRequestMetrics struct {
	Command     command `json:"command"`
	ElapsedTime float64 `json:"elapsedTime"`
	HasErr      bool    `json:"hasErr"`
}

// Response sent to the OVN CNI plugin by the Server
type Response struct {
	Result    *current.Result
	PodIFInfo *PodInterfaceInfo
	KubeAuth  *KubeAPIAuth
}

// PodRequest structure built from Request which is passed to the
// handler function given to the Server at creation time
type PodRequest struct {
	// The CNI command of the operation
	Command command
	// kubernetes namespace name
	PodNamespace string
	// kubernetes pod name
	PodName string
	// kubernetes pod UID
	PodUID string
	// kubernetes container ID
	SandboxID string
	// kernel network namespace path
	Netns string
	// Interface name to be configured
	IfName string
	// CNI conf obtained from stdin conf
	CNIConf *types.NetConf
	// Timestamp when the request was started
	timestamp time.Time
	// ctx is a context tracking this request's lifetime
	ctx context.Context
	// cancel should be called to cancel this request
	cancel context.CancelFunc

	podLister corev1listers.PodLister
	kclient   kubernetes.Interface
}

type cniRequestFunc func(request *PodRequest, podLister corev1listers.PodLister, useOVSExternalIDs bool, kclient kubernetes.Interface, kubeAuth *KubeAPIAuth) ([]byte, error)

// Server object that listens for JSON-marshaled Request objects
// on a private root-only Unix domain socket.
type Server struct {
	http.Server
	requestFunc       cniRequestFunc
	rundir            string
	useOVSExternalIDs int32
	podLister         corev1listers.PodLister
	kclient           kubernetes.Interface
	kubeAuth          *KubeAPIAuth
}
