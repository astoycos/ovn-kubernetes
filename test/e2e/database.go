package e2e

import (
	"context"
	"fmt"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/test/e2e/framework"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
)

var _ = ginkgo.Describe("e2e delete databases", func() {
	const (
		svcname                   string = "delete-db"
		ovnNs                     string = "ovn-kubernetes"
		databasePodPrefix         string = "ovnkube-db"
		databasePodNorthContainer string = "nb-ovsdb"
		northDBFileName           string = "ovnnb_db.db"
		southDBFileName           string = "ovnsb_db.db"
		dirDB                     string = "/etc/ovn"
		ovnWorkerNode             string = "ovn-worker"
		ovnWorkerNode2            string = "ovn-worker2"
		haModeMinDb               int    = 0
		haModeMaxDb               int    = 2
	)
	var (
		allDBFiles = []string{path.Join(dirDB, northDBFileName), path.Join(dirDB, southDBFileName)}
	)
	f := framework.NewDefaultFramework(svcname)

	// WaitForPodConditionAllowNotFoundError is a wrapper for WaitForPodCondition that allows at most 6 times for the pod not to be found.
	WaitForPodConditionAllowNotFoundErrors := func(f *framework.Framework, ns, podName, desc string, timeout time.Duration, condition podCondition) error {
		max_tries := 6               // 6 tries to waiting for the pod to restart
		cooldown := 10 * time.Second // 10 sec to cooldown between each try
		for i := 0; i < max_tries; i++ {
			err := e2epod.WaitForPodCondition(f.ClientSet, ns, podName, desc, 5*time.Minute, condition)
			if apierrors.IsNotFound(err) {
				// pod not found,try again after cooldown
				time.Sleep(cooldown)
				continue
			}
			if err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("Gave up after waiting %v for pod %q to be %q: pod is not found", timeout, podName, desc)
	}

	// waitForPodToFinishFullRestart waits for a the pod to finish it's reset cycle and returns.
	waitForPodToFinishFullRestart := func(f *framework.Framework, pod *v1.Pod) {
		err := e2epod.WaitForPodCondition(f.ClientSet, pod.Namespace, pod.Name, "not ready", 5*time.Minute, testutils.PodNotReady)
		if err != nil {
			framework.Failf("pod %v is not arrived to not-ready state: %v", pod.Name, err)
		}
		// during this stage on the restarting process we can encounter "pod not found" errors.
		// these types of errors are valid because the pod is restarting so there will be a period of time it is unavailable
		// so we will use "WaitForPodConditionAllowNotFoundErrors" in order to handle properly those errors.
		err = WaitForPodConditionAllowNotFoundErrors(f, pod.Namespace, pod.Name, "running and ready", 5*time.Minute, testutils.PodRunningReady)
		if err != nil {
			framework.Failf("pod %v is not arrived to running and ready state: %v", pod.Name, err)
		}
		return
	}

	deletePod := func(f *framework.Framework, namespace string, podName string) {
		podClient := f.ClientSet.CoreV1().Pods(namespace)
		err := podClient.Delete(context.Background(), podName, metav1.DeleteOptions{})
		framework.ExpectNoError(err, "failed to delete ovnkube-node pod")
	}

	fileExistsOnPod := func(f *framework.Framework, namespace string, pod *v1.Pod, file string) bool {
		containerFlag := fmt.Sprintf("-c=%s", pod.Spec.Containers[0].Name)
		_, err := framework.RunKubectl(ovnNs, "exec", pod.Name, containerFlag, "--", "ls", file)
		if err == nil {
			return true
		}
		if strings.Contains(err.Error(), fmt.Sprintf("ls: cannot access '%s': No such file or directory", file)) {
			return false
		}
		framework.Failf("failed to check if file %s exists on pod: %s, err: %v", file, pod.Name, err)
		return false
	}

	getDeployment := func(f *framework.Framework, namespace string, deploymentName string) *appsv1.Deployment {
		deploymentClient := f.ClientSet.AppsV1().Deployments(namespace)
		deployment, err := deploymentClient.Get(context.TODO(), deploymentName, metav1.GetOptions{})
		framework.ExpectNoError(err, "should get %s deployment", deploymentName)

		return deployment
	}

	allFilesExistsOnPod := func(f *framework.Framework, namespace string, pod *v1.Pod, files []string) bool {
		for _, file := range files {
			if !fileExistsOnPod(f, namespace, pod, file) {
				framework.Logf("file %s not exists", file)
				return false
			}
			framework.Logf("file %s exists", file)
		}
		return true
	}

	deleteFileFromPod := func(f *framework.Framework, namespace string, pod *v1.Pod, file string) {
		containerFlag := fmt.Sprintf("-c=%s", pod.Spec.Containers[0].Name)
		framework.RunKubectl(ovnNs, "exec", pod.Name, containerFlag, "--", "rm", file)
		if fileExistsOnPod(f, namespace, pod, file) {
			framework.Failf("Error: failed to delete file %s", file)
		}
		framework.Logf("file %s deleted ", file)
	}

	singlePodConnectivityTest := func(f *framework.Framework, podName string) {
		framework.Logf("Running container which tries to connect to API server in a loop")
		podChan, errChan := make(chan *v1.Pod), make(chan error)

		go func() {
			defer ginkgo.GinkgoRecover()
			checkContinuousConnectivity(f, "", podName, getApiAddress(), 443, 30, podChan, errChan)
		}()
		testPod := <-podChan

		framework.Logf("Test pod running on %q", testPod.Spec.NodeName)
		framework.ExpectNoError(<-errChan)
	}

	twoPodsContinuousConnectivityTest := func(f *framework.Framework,
		node1Name string, node2Name string,
		syncChan chan string, errChan chan error) {
		const (
			pod1Name                  string        = "connectivity-test-pod1"
			pod2Name                  string        = "connectivity-test-pod2"
			port                      string        = "8080"
			timeIntervalBetweenChecks time.Duration = 2 * time.Second
		)

		var (
			command = []string{"/agnhost", "netexec", fmt.Sprintf("--http-port=" + port)}
		)
		createGenericPod(f, pod1Name, node1Name, f.Namespace.Name, command)
		createGenericPod(f, pod2Name, node2Name, f.Namespace.Name, command)

		pod2IP := getPodAddress(pod2Name, f.Namespace.Name)

		ginkgo.By("Checking initial connectivity from one pod to the other and verifying that the connection is achieved")

		stdout, err := framework.RunKubectl(f.Namespace.Name, "exec", pod1Name, "--", "curl", fmt.Sprintf("%s/hostname", net.JoinHostPort(pod2IP, port)))

		if err != nil || stdout != pod2Name {
			framework.Failf("Error: attempted connection to pod %s found err:  %v", pod2Name, err)
		}

		syncChan <- "connectivity test pods are ready"

	L:
		for {
			select {
			case msg := <-syncChan:
				framework.Logf(msg + "finish connectivity test.")
				break L
			default:
				stdout, err := framework.RunKubectl(f.Namespace.Name, "exec", pod1Name, "--", "curl", fmt.Sprintf("%s/hostname", net.JoinHostPort(pod2IP, port)))
				if err != nil || stdout != pod2Name {
					errChan <- err
					framework.Failf("Error: attempted connection to pod %s found err:  %v", pod2Name, err)
				}
				time.Sleep(timeIntervalBetweenChecks)
			}
		}

		errChan <- nil
	}

	table.DescribeTable("recovering from deleting db files while maintain connectivity",
		func(db_pod_num int, DBFileNamesToDelete []string) {
			var (
				db_pod_name = fmt.Sprintf("%s-%d", databasePodPrefix, db_pod_num)
			)
			if db_pod_num < haModeMinDb || db_pod_num > haModeMaxDb {
				framework.Failf("invalid db_pod_num.")
				return
			}

			// Adding db file path
			for i, file := range DBFileNamesToDelete {
				DBFileNamesToDelete[i] = path.Join(dirDB, file)
			}

			framework.Logf("connectivity test before deleting db files")
			framework.Logf("test simple connectivity from new pod to API server,before deleting db files")
			singlePodConnectivityTest(f, "before-delete-db-files")
			framework.Logf("setup two pods for continues connectivity test ")
			syncChan, errChan := make(chan string), make(chan error)
			go func() {
				defer ginkgo.GinkgoRecover()
				twoPodsContinuousConnectivityTest(f, ovnWorkerNode, ovnWorkerNode2, syncChan, errChan)
			}()
			//wait for the connectivity test pods to be ready
			framework.Logf(<-syncChan + "delete and restart db pods.")

			// Start the db disruption - delete the db files and delete the db-pod in order to emulate the cluster/pod restart

			// Retrive the DB pod
			dbPod, err := f.ClientSet.CoreV1().Pods(ovnNs).Get(context.Background(), db_pod_name, metav1.GetOptions{})
			framework.ExpectNoError(err, fmt.Sprintf("unable to get pod: %s, err: %v", db_pod_name, err))

			// Check that all files exists on pod
			framework.Logf("make sure that all the db files are exists on the db pod:%s", dbPod.Name)
			if !allFilesExistsOnPod(f, ovnNs, dbPod, allDBFiles) {
				framework.Failf("Error: db files not found")
			}
			// Delete the db files from the db-pod
			framework.Logf("deleting db files from db pod")
			for _, db_file := range DBFileNamesToDelete {
				deleteFileFromPod(f, ovnNs, dbPod, db_file)
			}
			// Delete the db-pod in order to emulate the cluster/pod restart
			framework.Logf("deleting db pod:%s", dbPod.Name)
			deletePod(f, ovnNs, dbPod.Name)

			framework.Logf("wait for db pod to finish full restart")
			waitForPodToFinishFullRestart(f, dbPod)

			// Check db files existence
			// Check that all files exists on pod
			framework.Logf("make sure that all the db files are exists on the db pod:%s", dbPod.Name)
			if !allFilesExistsOnPod(f, ovnNs, dbPod, allDBFiles) {
				framework.Failf("Error: db files not found")
			}

			// disruption over.
			syncChan <- "disruption over."
			framework.ExpectNoError(<-errChan)

			framework.Logf("test simple connectivity from new pod to API server,after recovery")
			singlePodConnectivityTest(f, "after-delete-db-files")
		},

		// One can choose to delete only specific db file (uncomment the requested lines)

		// db pod 0
		table.Entry("when delete both db files on ovnkube-db-0", 0, []string{northDBFileName, southDBFileName}),
		// table.Entry("when delete north db on ovnkube-db-0", 0, []string{northDBFileName}),
		// table.Entry("when delete south db on ovnkube-db-0", 0, []string{southDBFileName}),

		// db pod 1
		table.Entry("when delete both db files on ovnkube-db-1", 1, []string{northDBFileName, southDBFileName}),
		// table.Entry("when delete north db on ovnkube-db-1", 1, []string{northDBFileName}),
		// table.Entry("when delete south db on ovnkube-db-1", 1, []string{southDBFileName}),

		// db pod 2
		table.Entry("when delete both db files on ovnkube-db-2", 2, []string{northDBFileName, southDBFileName}),
		// table.Entry("when delete north db on ovnkube-db-2", 2, []string{northDBFileName}),
		// table.Entry("when delete south db on ovnkube-db-2", 2, []string{southDBFileName}),
	)

	ginkgo.It("Should validate connectivity before and after deleting all the db-pods at once in Non-HA mode", func() {
		dbDeployment := getDeployment(f, ovnNs, "ovnkube-db")
		dbPods, err := e2edeployment.GetPodsForDeployment(f.ClientSet, dbDeployment)
		if err != nil {
			framework.Failf("Error: Failed to get pods, err: %v", err)
		}
		if dbPods.Size() == 0 {
			framework.Failf("Error: db pods not found")
		}

		framework.Logf("test simple connectivity from new pod to API server,before deleting db pods")
		singlePodConnectivityTest(f, "before-delete-db-pods")

		framework.Logf("deleting all the db pods")

		for _, dbPod := range dbPods.Items {
			dbPodName := dbPod.Name
			framework.Logf("deleting db pod: %v", dbPodName)
			// Delete the db-pod in order to emulate the pod restart
			dbPod.Status.Message = "check"
			deletePod(f, ovnNs, dbPodName)
			e2epod.WaitForPodTerminatedInNamespace(f.ClientSet, dbPodName, "", ovnNs)
		}

		framework.Logf("wait for all the Deployment to become ready again after pod deletion")
		e2edeployment.WaitForDeploymentComplete(f.ClientSet, dbDeployment)

		framework.Logf("all the pods finish full restart")

		framework.Logf("test simple connectivity from new pod to API server,after recovery")
		singlePodConnectivityTest(f, "after-delete-db-pods")
	})

	ginkgo.It("Should validate connectivity before and after deleting all the db-pods at once in HA mode", func() {
		dbPods, err := e2epod.GetPods(f.ClientSet, ovnNs, map[string]string{"name": databasePodPrefix})
		if err != nil {
			framework.Failf("Error: Failed to get pods, err: %v", err)
		}
		if len(dbPods) == 0 {
			framework.Failf("Error: db pods not found")
		}

		framework.Logf("test simple connectivity from new pod to API server,before deleting db pods")
		singlePodConnectivityTest(f, "before-delete-db-pods")

		framework.Logf("deleting all the db pods")
		for _, dbPod := range dbPods {
			dbPodName := dbPod.Name
			framework.Logf("deleting db pod: %v", dbPodName)
			// Delete the db-pod in order to emulate the pod restart
			dbPod.Status.Message = "check"
			deletePod(f, ovnNs, dbPodName)
		}

		framework.Logf("wait for all the pods to finish full restart")
		var wg sync.WaitGroup
		for _, pod := range dbPods {
			wg.Add(1)
			go func(pod v1.Pod) {
				defer ginkgo.GinkgoRecover()
				defer wg.Done()
				waitForPodToFinishFullRestart(f, &pod)
			}(pod)
		}
		wg.Wait()
		framework.Logf("all the pods finish full restart")

		framework.Logf("test simple connectivity from new pod to API server,after recovery")
		singlePodConnectivityTest(f, "after-delete-db-pods")
	})
})
