package ovn

import (
	"context"
	"encoding/json"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	"github.com/urfave/cli/v2"

	v1 "k8s.io/api/core/v1"

	"github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/extensions/table"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("OVN Egress Gateway Operations", func() {
	const (
		namespaceName = "namespace1"
	)
	var (
		app               *cli.App
		fakeOvn           *FakeOVN
		fExec             *ovntest.FakeExec
		bfd1NamedUUID     = "bfd-1-UUID"
		bfd2NamedUUID     = "bfd-2-UUID"
		logicalRouterPort = "rtoe-GR_node1"
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags

		fExec = ovntest.NewLooseCompareFakeExec()
		fakeOvn = NewFakeOVN(fExec)
	})

	ginkgo.AfterEach(func() {
		fakeOvn.shutdown()
	})

	ginkgo.Context("on setting namespace gateway annotations", func() {

		table.DescribeTable("reconciles an new pod with namespace single exgw annotation already set", func(bfd bool, finalNB []libovsdb.TestData) {
			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1"}
				if bfd {
					namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				}
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)

				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)

				t.populateLogicalSwitchCache(fakeOvn)

				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()

				gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}, table.Entry("No BFD", false, []libovsdb.TestData{
			&nbdb.LogicalRouterStaticRoute{
				UUID:       "static-route-1-UUID",
				IPPrefix:   "10.128.1.3/32",
				Nexthop:    "9.0.0.1",
				Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
				OutputPort: &logicalRouterPort,
				Options: map[string]string{
					"ecmp_symmetric_reply": "true",
				},
			},
			&nbdb.LogicalRouter{
				UUID:         "GR_node1-UUID",
				Name:         "GR_node1",
				StaticRoutes: []string{"static-route-1-UUID"},
			},
		}),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "9.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID"},
				},
			}))

		table.DescribeTable("reconciles an new pod with namespace single exgw annotation already set with pod event first", func(bfd bool, finalNB []libovsdb.TestData) {
			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1"}
				if bfd {
					namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				}
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)

				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)

				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()

				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Create(context.TODO(), &namespaceT, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}, table.Entry("No BFD", false, []libovsdb.TestData{
			&nbdb.LogicalRouterStaticRoute{
				UUID:       "static-route-1-UUID",
				IPPrefix:   "10.128.1.3/32",
				Nexthop:    "9.0.0.1",
				Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
				OutputPort: &logicalRouterPort,
				Options: map[string]string{
					"ecmp_symmetric_reply": "true",
				},
			},
			&nbdb.LogicalRouter{
				UUID:         "GR_node1-UUID",
				Name:         "GR_node1",
				StaticRoutes: []string{"static-route-1-UUID"},
			},
		}),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "9.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID"},
				},
			}))

		table.DescribeTable("reconciles an new pod with namespace double exgw annotation already set", func(bfd bool, finalNB []libovsdb.TestData) {

			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1,9.0.0.2"}
				if bfd {
					namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				}
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)

				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)

				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()

				gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		},
			table.Entry("No BFD", false, []libovsdb.TestData{
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-2-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.2",
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
				},
			}),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "9.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.BFD{
					UUID:        bfd2NamedUUID,
					DstIP:       "9.0.0.2",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-2-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.2",
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					BFD:        &bfd2NamedUUID,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
				},
			}),
		)

		table.DescribeTable("reconciles deleting a pod with namespace double exgw annotation already set",
			func(bfd bool,
				initNB []libovsdb.TestData,
				finalNB []libovsdb.TestData,
			) {
				app.Action = func(ctx *cli.Context) error {

					namespaceT := *newNamespace("namespace1")
					namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1,9.0.0.2"}
					if bfd {
						namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
					}
					t := newTPod(
						"node1",
						"10.128.1.0/24",
						"10.128.1.2",
						"10.128.1.1",
						"myPod",
						"10.128.1.3",
						"0a:58:0a:80:01:03",
						namespaceT.Name,
					)

					t.baseCmds(fExec)

					fakeOvn.startWithDBSetup(ctx,
						libovsdbtest.TestSetup{
							NBData: initNB,
						},
						&v1.NamespaceList{
							Items: []v1.Namespace{
								namespaceT,
							},
						},
						&v1.PodList{
							Items: []v1.Pod{
								*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
							},
						},
					)
					t.populateLogicalSwitchCache(fakeOvn)

					injectNode(fakeOvn)
					fakeOvn.controller.WatchNamespaces()
					fakeOvn.controller.WatchPods()

					gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))
					gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

					err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Delete(context.TODO(), t.podName, *metav1.NewDeleteOptions(0))
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
					gomega.Eventually(fakeOvn.nbClient).Should(libovsdbtest.HaveData(finalNB))
					return nil
				}
				err := app.Run([]string{app.Name})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			},
			table.Entry("No BFD", false,
				[]libovsdb.TestData{
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-2-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.2",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
					},
				},
				[]libovsdb.TestData{
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{},
					},
				},
			),
			table.Entry("BFD", true,
				[]libovsdb.TestData{
					&nbdb.BFD{
						UUID:        bfd1NamedUUID,
						DstIP:       "9.0.0.1",
						LogicalPort: "rtoe-GR_node1",
					},
					&nbdb.BFD{
						UUID:        bfd2NamedUUID,
						DstIP:       "9.0.0.2",
						LogicalPort: "rtoe-GR_node1",
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						BFD:        &bfd1NamedUUID,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-2-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.2",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						BFD:        &bfd2NamedUUID,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
					},
				},
				[]libovsdb.TestData{
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{},
					},
				},
			),
		)

		// table.DescribeTable("reconciles deleting a pod with namespace double exgw annotation already set IPV6",
		// 	func(bfd bool,
		// 		nbctlOnAddCommands []string,
		// 		nbctlOnDelCommands []struct {
		// 			command string
		// 			res     string
		// 		},
		// 		libovsdbInput []libovsdb.TestData,
		// 		libovsdbOutput []libovsdb.TestData,
		// 	) {
		// 		app.Action = func(ctx *cli.Context) error {

		// 			namespaceT := *newNamespace("namespace1")
		// 			namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "fd2e:6f44:5dd8::89,fd2e:6f44:5dd8::76"}
		// 			if bfd {
		// 				namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
		// 			}
		// 			t := newTPod(
		// 				"node1",
		// 				"fd00:10:244:2::0/64",
		// 				"fd00:10:244:2::2",
		// 				"fd00:10:244:2::1",
		// 				"myPod",
		// 				"fd00:10:244:2::3",
		// 				"0a:58:49:a1:93:cb",
		// 				namespaceT.Name,
		// 			)

		// 			t.baseCmds(fExec)

		// 			fakeOvn.startWithDBSetup(ctx,
		// 				libovsdbtest.TestSetup{
		// 					NBData: libovsdbInput,
		// 				},
		// 				&v1.NamespaceList{
		// 					Items: []v1.Namespace{
		// 						namespaceT,
		// 					},
		// 				},
		// 				&v1.PodList{
		// 					Items: []v1.Pod{
		// 						*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
		// 					},
		// 				},
		// 			)
		// 			t.populateLogicalSwitchCache(fakeOvn)
		// 			for _, cmd := range nbctlOnAddCommands {
		// 				fExec.AddFakeCmd(&ovntest.ExpectedCmd{
		// 					Cmd:    cmd,
		// 					Output: "\n",
		// 				})
		// 			}
		// 			injectNode(fakeOvn)
		// 			fakeOvn.controller.WatchNamespaces()
		// 			fakeOvn.controller.WatchPods()

		// 			gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/64"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/64", "gateway_ip": "` + t.nodeGWIP + `"}}`))
		// 			gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

		// 			for _, cmd := range nbctlOnDelCommands {
		// 				fExec.AddFakeCmd(&ovntest.ExpectedCmd{
		// 					Cmd:    cmd.command,
		// 					Output: cmd.res,
		// 				})
		// 			}

		// 			err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Delete(context.TODO(), t.podName, *metav1.NewDeleteOptions(0))
		// 			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// 			gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
		// 			gomega.Eventually(fakeOvn.nbClient).Should(libovsdbtest.HaveData(libovsdbOutput))
		// 			return nil
		// 		}
		// 		err := app.Run([]string{app.Name})
		// 		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// 	},
		// 	table.Entry("No BFD IPV6", false, []string{
		// 		"ovn-nbctl --timeout=15 --may-exist --bfd --policy=src-ip --ecmp-symmetric-reply lr-route-add GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::89 rtoe-GR_node1",
		// 		"ovn-nbctl --timeout=15 --may-exist --bfd --policy=src-ip --ecmp-symmetric-reply lr-route-add GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::76 rtoe-GR_node1",
		// 	},
		// 		[]struct {
		// 			command string
		// 			res     string
		// 		}{
		// 			{"ovn-nbctl --timeout=15 --if-exists --policy=src-ip lr-route-del GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::89", "\n"},
		// 			{"ovn-nbctl --timeout=15 --if-exists --policy=src-ip lr-route-del GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::76", "\n"},
		// 		},
		// 		[]libovsdb.TestData{},
		// 		[]libovsdb.TestData{},
		// 	),
		// 	table.Entry("BFD IPV6", true, []string{
		// 		"ovn-nbctl --timeout=15 --may-exist --bfd --policy=src-ip --ecmp-symmetric-reply lr-route-add GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::89 rtoe-GR_node1",
		// 		"ovn-nbctl --timeout=15 --may-exist --bfd --policy=src-ip --ecmp-symmetric-reply lr-route-add GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::76 rtoe-GR_node1",
		// 	},
		// 		[]struct {
		// 			command string
		// 			res     string
		// 		}{
		// 			{"ovn-nbctl --timeout=15 --if-exists --policy=src-ip lr-route-del GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::89", "\n"},
		// 			{"ovn-nbctl --timeout=15 --if-exists --policy=src-ip lr-route-del GR_node1 fd00:10:244:2::3/128 fd2e:6f44:5dd8::76", "\n"},
		// 		},
		// 		[]libovsdb.TestData{
		// 			&nbdb.LogicalRouterStaticRoute{
		// 				UUID:       "static-route-UUID",
		// 				BFD:        &bfd1NamedUUID,
		// 				OutputPort: &logicalRouterPort,
		// 				Nexthop:    "fd2e:6f44:5dd8::89",
		// 			},
		// 			&nbdb.BFD{
		// 				UUID:        bfd2NamedUUID,
		// 				DstIP:       "fd2e:6f44:5dd8::76",
		// 				LogicalPort: "rtoe-GR_node1",
		// 			},
		// 			&nbdb.BFD{
		// 				UUID:        bfd1NamedUUID,
		// 				DstIP:       "fd2e:6f44:5dd8::89",
		// 				LogicalPort: "rtoe-GR_node1",
		// 			},
		// 		},
		// 		[]libovsdb.TestData{
		// 			&nbdb.LogicalRouterStaticRoute{
		// 				UUID:       "static-route-UUID",
		// 				BFD:        &bfd1NamedUUID,
		// 				OutputPort: &logicalRouterPort,
		// 				Nexthop:    "fd2e:6f44:5dd8::89",
		// 			},
		// 			&nbdb.BFD{
		// 				UUID:        bfd1NamedUUID,
		// 				DstIP:       "fd2e:6f44:5dd8::89",
		// 				LogicalPort: "rtoe-GR_node1",
		// 			},
		// 		},
		// 	),
		// )

		table.DescribeTable("reconciles deleting a exgw namespace with active pod",
			func(bfd bool,
				initNB []libovsdb.TestData,
				finalNB []libovsdb.TestData,
			) {
				app.Action = func(ctx *cli.Context) error {

					namespaceT := *newNamespace("namespace1")
					namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1,9.0.0.2"}
					if bfd {
						namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
					}
					t := newTPod(
						"node1",
						"10.128.1.0/24",
						"10.128.1.2",
						"10.128.1.1",
						"myPod",
						"10.128.1.3",
						"0a:58:0a:80:01:03",
						namespaceT.Name,
					)

					t.baseCmds(fExec)
					fakeOvn.startWithDBSetup(ctx,
						libovsdbtest.TestSetup{
							NBData: initNB,
						},
						&v1.NamespaceList{
							Items: []v1.Namespace{
								namespaceT,
							},
						},
						&v1.PodList{
							Items: []v1.Pod{
								*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
							},
						},
					)
					t.populateLogicalSwitchCache(fakeOvn)

					injectNode(fakeOvn)
					fakeOvn.controller.WatchNamespaces()
					fakeOvn.controller.WatchPods()

					gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))

					err := fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Delete(context.TODO(), t.namespace, *metav1.NewDeleteOptions(0))
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
					gomega.Eventually(fakeOvn.nbClient).Should(libovsdbtest.HaveData(finalNB))
					return nil
				}

				err := app.Run([]string{app.Name})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			},
			table.Entry("No BFD", false,
				[]libovsdb.TestData{
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-2-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.2",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
					},
				},
				[]libovsdb.TestData{
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{},
					},
				},
			),
			table.Entry("BFD", true,
				[]libovsdb.TestData{
					&nbdb.BFD{
						UUID:        "bfd1-UUID",
						DstIP:       "9.0.0.1",
						LogicalPort: "rtoe-GR_node1",
					},
					&nbdb.BFD{
						UUID:        "bfd2-UUID",
						DstIP:       "9.0.0.2",
						LogicalPort: "rtoe-GR_node1",
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						BFD:        &bfd1NamedUUID,
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-2-UUID",
						IPPrefix:   "10.128.1.3/32",
						BFD:        &bfd2NamedUUID,
						Nexthop:    "9.0.0.2",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
					},
				},
				[]libovsdb.TestData{
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{},
					},
				},
			))
	})

	ginkgo.Context("on setting pod gateway annotations", func() {
		table.DescribeTable("reconciles a host networked pod acting as a exgw for another namespace for new pod", func(bfd bool, finalNB []libovsdb.TestData) {
			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceX := *newNamespace("namespace2")
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				gwPod := *newPod(namespaceX.Name, "gwPod", "node2", "9.0.0.1")
				gwPod.Annotations = map[string]string{"k8s.ovn.org/routing-namespaces": namespaceT.Name}
				if bfd {
					gwPod.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				}
				gwPod.Spec.HostNetwork = true
				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT, namespaceX,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							gwPod,
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)
				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()

				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Create(context.TODO(), newPod(t.namespace, t.podName, t.nodeName, t.podIP), metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}, table.Entry("No BFD", false, []libovsdb.TestData{
			&nbdb.LogicalRouterStaticRoute{
				UUID:       "static-route-1-UUID",
				IPPrefix:   "10.128.1.3/32",
				Nexthop:    "9.0.0.1",
				Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
				OutputPort: &logicalRouterPort,
				Options: map[string]string{
					"ecmp_symmetric_reply": "true",
				},
			},
			&nbdb.LogicalRouter{
				UUID:         "GR_node1-UUID",
				Name:         "GR_node1",
				StaticRoutes: []string{"static-route-1-UUID"},
			},
		}),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "9.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID"},
				},
			}))

		table.DescribeTable("reconciles a host networked pod acting as a exgw for another namespace for existing pod", func(bfd bool, finalNB []libovsdb.TestData) {
			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceX := *newNamespace("namespace2")
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				gwPod := *newPod(namespaceX.Name, "gwPod", "node2", "9.0.0.1")
				gwPod.Annotations = map[string]string{"k8s.ovn.org/routing-namespaces": namespaceT.Name}
				if bfd {
					gwPod.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				}
				gwPod.Spec.HostNetwork = true
				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT, namespaceX,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)
				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)

				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(namespaceX.Name).Create(context.TODO(), &gwPod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}, table.Entry("No BFD", false, []libovsdb.TestData{
			&nbdb.LogicalRouterStaticRoute{
				UUID:       "static-route-1-UUID",
				IPPrefix:   "10.128.1.3/32",
				Nexthop:    "9.0.0.1",
				Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
				OutputPort: &logicalRouterPort,
				Options: map[string]string{
					"ecmp_symmetric_reply": "true",
				},
			},
			&nbdb.LogicalRouter{
				UUID:         "GR_node1-UUID",
				Name:         "GR_node1",
				StaticRoutes: []string{"static-route-1-UUID"},
			},
		}),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "9.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID"},
				},
			}))

		table.DescribeTable("reconciles a multus networked pod acting as a exgw for another namespace for new pod", func(bfd bool, finalNB []libovsdb.TestData) {
			app.Action = func(ctx *cli.Context) error {
				ns := nettypes.NetworkStatus{Name: "dummy", IPs: []string{"11.0.0.1"}}
				networkStatuses := []nettypes.NetworkStatus{ns}
				nsEncoded, err := json.Marshal(networkStatuses)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				namespaceT := *newNamespace("namespace1")
				namespaceX := *newNamespace("namespace2")
				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				gwPod := *newPod(namespaceX.Name, "gwPod", "node2", "9.0.0.1")
				gwPod.Annotations = map[string]string{
					"k8s.ovn.org/routing-namespaces":    namespaceT.Name,
					"k8s.ovn.org/routing-network":       "dummy",
					"k8s.v1.cni.cncf.io/network-status": string(nsEncoded),
				}
				if bfd {
					gwPod.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				}
				gwPod.Spec.HostNetwork = true
				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdb.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT, namespaceX,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							gwPod,
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)
				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()

				_, err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(t.namespace).Create(context.TODO(), newPod(t.namespace, t.podName, t.nodeName, t.podIP), metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Eventually(func() string { return getPodAnnotations(fakeOvn.fakeClient.KubeClient, t.namespace, t.podName) }, 2).Should(gomega.MatchJSON(`{"default": {"ip_addresses":["` + t.podIP + `/24"], "mac_address":"` + t.podMAC + `", "gateway_ips": ["` + t.nodeGWIP + `"], "ip_address":"` + t.podIP + `/24", "gateway_ip": "` + t.nodeGWIP + `"}}`))
				gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}, table.Entry("No BFD", false, []libovsdb.TestData{
			&nbdb.LogicalRouterStaticRoute{
				UUID:       "static-route-1-UUID",
				IPPrefix:   "10.128.1.3/32",
				Nexthop:    "11.0.0.1",
				Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
				OutputPort: &logicalRouterPort,
				Options: map[string]string{
					"ecmp_symmetric_reply": "true",
				},
			},
			&nbdb.LogicalRouter{
				UUID:         "GR_node1-UUID",
				Name:         "GR_node1",
				StaticRoutes: []string{"static-route-1-UUID"},
			},
		}),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "11.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "11.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID"},
				},
			}))

		table.DescribeTable("reconciles deleting a host networked pod acting as a exgw for another namespace for existing pod",
			func(bfd bool,
				beforeDeleteNB []libovsdb.TestData,
				afterDeleteNB []libovsdb.TestData) {
				app.Action = func(ctx *cli.Context) error {

					namespaceT := *newNamespace("namespace1")
					namespaceX := *newNamespace("namespace2")
					t := newTPod(
						"node1",
						"10.128.1.0/24",
						"10.128.1.2",
						"10.128.1.1",
						"myPod",
						"10.128.1.3",
						"0a:58:0a:80:01:03",
						namespaceT.Name,
					)
					gwPod := *newPod(namespaceX.Name, "gwPod", "node2", "9.0.0.1")
					gwPod.Annotations = map[string]string{"k8s.ovn.org/routing-namespaces": namespaceT.Name}
					if bfd {
						gwPod.Annotations["k8s.ovn.org/bfd-enabled"] = ""
					}
					gwPod.Spec.HostNetwork = true
					t.baseCmds(fExec)
					fakeOvn.startWithDBSetup(ctx,
						libovsdbtest.TestSetup{
							NBData: []libovsdbtest.TestData{
								&nbdb.LogicalRouter{
									UUID: "GR_node1-UUID",
									Name: "GR_node1",
								},
							},
						},
						&v1.NamespaceList{
							Items: []v1.Namespace{
								namespaceT, namespaceX,
							},
						},
						&v1.PodList{
							Items: []v1.Pod{
								*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
							},
						},
					)
					t.populateLogicalSwitchCache(fakeOvn)
					injectNode(fakeOvn)
					fakeOvn.controller.WatchNamespaces()
					fakeOvn.controller.WatchPods()

					_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(namespaceX.Name).Create(context.TODO(), &gwPod, metav1.CreateOptions{})
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
					gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(beforeDeleteNB))

					err = fakeOvn.fakeClient.KubeClient.CoreV1().Pods(namespaceX.Name).Delete(context.TODO(), gwPod.Name, *metav1.NewDeleteOptions(0))
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Eventually(fExec.CalledMatchesExpected).Should(gomega.BeTrue(), fExec.ErrorDesc)
					gomega.Eventually(fakeOvn.nbClient).Should(libovsdb.HaveData(afterDeleteNB))
					return nil
				}

				err := app.Run([]string{app.Name})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
			},
			table.Entry("No BFD", false,
				[]libovsdb.TestData{
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID"},
					},
				},
				[]libovsdb.TestData{
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{},
					},
				},
			),
			table.Entry("BFD Enabled", true, []libovsdb.TestData{
				&nbdb.BFD{
					UUID:        bfd1NamedUUID,
					DstIP:       "9.0.0.1",
					LogicalPort: "rtoe-GR_node1",
				},
				&nbdb.LogicalRouterStaticRoute{
					UUID:       "static-route-1-UUID",
					IPPrefix:   "10.128.1.3/32",
					Nexthop:    "9.0.0.1",
					BFD:        &bfd1NamedUUID,
					Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
					OutputPort: &logicalRouterPort,
					Options: map[string]string{
						"ecmp_symmetric_reply": "true",
					},
				},
				&nbdb.LogicalRouter{
					UUID:         "GR_node1-UUID",
					Name:         "GR_node1",
					StaticRoutes: []string{"static-route-1-UUID"},
				},
			},
				[]libovsdb.TestData{
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{},
					},
				},
			),
		)
	})
	ginkgo.Context("on using bfd", func() {
		ginkgo.It("should enable bfd only on the namespace gw when set", func() {
			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1"}
				namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""
				namespaceX := *newNamespace("namespace2")

				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				gwPod := *newPod(namespaceX.Name, "gwPod", "node2", "10.0.0.1")
				gwPod.Annotations = map[string]string{"k8s.ovn.org/routing-namespaces": namespaceT.Name}
				gwPod.Spec.HostNetwork = true
				t.baseCmds(fExec)

				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)

				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(namespaceX.Name).Create(context.TODO(), &gwPod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				finalNB := []libovsdb.TestData{
					&nbdb.BFD{
						UUID:        bfd1NamedUUID,
						DstIP:       "9.0.0.1",
						LogicalPort: "rtoe-GR_node1",
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						BFD:        &bfd1NamedUUID,
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-2-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "10.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
					},
				}
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdbtest.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		ginkgo.It("should enable bfd only on the gw pod when set", func() {
			app.Action = func(ctx *cli.Context) error {

				namespaceT := *newNamespace("namespace1")
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1"}
				namespaceX := *newNamespace("namespace2")

				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)
				gwPod := *newPod(namespaceX.Name, "gwPod", "node2", "10.0.0.1")
				gwPod.Annotations = map[string]string{"k8s.ovn.org/routing-namespaces": namespaceT.Name}
				gwPod.Annotations["k8s.ovn.org/bfd-enabled"] = ""

				gwPod.Spec.HostNetwork = true
				t.baseCmds(fExec)

				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.LogicalRouter{
								UUID: "GR_node1-UUID",
								Name: "GR_node1",
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)

				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Pods(namespaceX.Name).Create(context.TODO(), &gwPod, metav1.CreateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				finalNB := []libovsdb.TestData{
					&nbdb.BFD{
						UUID:        bfd1NamedUUID,
						DstIP:       "10.0.0.1",
						LogicalPort: "rtoe-GR_node1",
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-2-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "10.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						BFD:        &bfd1NamedUUID,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID", "static-route-2-UUID"},
					},
				}
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdbtest.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		ginkgo.It("should disable bfd when removing the annotation from the namespace", func() {
			app.Action = func(ctx *cli.Context) error {
				namespaceT := *newNamespace("namespace1")
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1"}
				namespaceT.Annotations["k8s.ovn.org/bfd-enabled"] = ""

				t := newTPod(
					"node1",
					"10.128.1.0/24",
					"10.128.1.2",
					"10.128.1.1",
					"myPod",
					"10.128.1.3",
					"0a:58:0a:80:01:03",
					namespaceT.Name,
				)

				t.baseCmds(fExec)
				fakeOvn.startWithDBSetup(ctx,
					libovsdbtest.TestSetup{
						NBData: []libovsdbtest.TestData{
							&nbdb.BFD{
								UUID:        bfd1NamedUUID,
								DstIP:       "9.0.0.1",
								LogicalPort: "rtoe-GR_node1",
							},
							&nbdb.LogicalRouterStaticRoute{
								UUID:       "static-route-1-UUID",
								IPPrefix:   "10.128.1.3/32",
								Nexthop:    "9.0.0.1",
								Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
								BFD:        &bfd1NamedUUID,
								OutputPort: &logicalRouterPort,
								Options: map[string]string{
									"ecmp_symmetric_reply": "true",
								},
							},
							&nbdb.LogicalRouter{
								UUID:         "GR_node1-UUID",
								Name:         "GR_node1",
								StaticRoutes: []string{"static-route-1-UUID"},
							},
						},
					},
					&v1.NamespaceList{
						Items: []v1.Namespace{
							namespaceT,
						},
					},
					&v1.PodList{
						Items: []v1.Pod{
							*newPod(t.namespace, t.podName, t.nodeName, t.podIP),
						},
					},
				)
				t.populateLogicalSwitchCache(fakeOvn)

				injectNode(fakeOvn)
				fakeOvn.controller.WatchNamespaces()
				fakeOvn.controller.WatchPods()
				namespaceT.Annotations = map[string]string{"k8s.ovn.org/routing-external-gws": "9.0.0.1"}
				_, err := fakeOvn.fakeClient.KubeClient.CoreV1().Namespaces().Update(context.Background(), &namespaceT, metav1.UpdateOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				finalNB := []libovsdb.TestData{
					&nbdb.LogicalRouterStaticRoute{
						UUID:       "static-route-1-UUID",
						IPPrefix:   "10.128.1.3/32",
						Nexthop:    "9.0.0.1",
						Policy:     &nbdb.LogicalRouterStaticRoutePolicySrcIP,
						OutputPort: &logicalRouterPort,
						Options: map[string]string{
							"ecmp_symmetric_reply": "true",
						},
					},
					&nbdb.LogicalRouter{
						UUID:         "GR_node1-UUID",
						Name:         "GR_node1",
						StaticRoutes: []string{"static-route-1-UUID"},
					},
				}
				gomega.Eventually(fakeOvn.nbClient).Should(libovsdbtest.HaveData(finalNB))
				return nil
			}

			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})
})

// injectNode adds a valid node to the nodeinformer so the get
// to understand if there are two bridged won't fail
func injectNode(fakeOvn *FakeOVN) {
	node := &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			Annotations: map[string]string{"k8s.ovn.org/l3-gateway-config": `{"default":{"mode":"local","mac-address":"7e:57:f8:f0:3c:49", "ip-address":"169.254.33.2/24", "next-hop":"169.254.33.1"}}`,
				"k8s.ovn.org/node-chassis-id": "79fdcfc4-6fe6-4cd3-8242-c0f85a4668ec",
			},
		},
	}
	fakeOvn.controller.watchFactory.NodeInformer().GetStore().Add(node)
}
