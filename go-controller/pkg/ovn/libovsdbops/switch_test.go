package libovsdbops

import (
	"fmt"
	"testing"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
)

func TestRemoveACLFromNodeSwitches(t *testing.T) {
	fakeACL := &nbdb.ACL{
		UUID: "a08ea426-2288-11eb-a30b-a8a1590cda29",
	}

	fakeSwitch1 := &nbdb.LogicalSwitch{
		Name: "sw1",
		//UUID: "sw1-uuid",
		ACLs: []string{fakeACL.UUID},
	}

	fakeSwitch2 := &nbdb.LogicalSwitch{
		Name: "sw2",
		//UUID: "sw2-uuid",
		ACLs: []string{fakeACL.UUID},
	}

	tests := []struct {
		desc         string
		aclUUID      string
		expectErr    bool
		initialNbdb  libovsdbtest.TestSetup
		expectedNbdb libovsdbtest.TestSetup
	}{
		{
			desc:      "remove acl on two switches",
			aclUUID:   "a08ea426-2288-11eb-a30b-a8a1590cda29",
			expectErr: false,
			initialNbdb: libovsdbtest.TestSetup{
				NBData: []libovsdbtest.TestData{
					fakeSwitch1,
					fakeSwitch2,
				},
			},
			expectedNbdb: libovsdbtest.TestSetup{
				NBData: []libovsdbtest.TestData{
					&nbdb.LogicalSwitch{
						Name: "sw1",
						UUID: "sw1-uuid",
					},
					&nbdb.LogicalSwitch{
						Name: "sw2",
						UUID: "sw2-uuid",
					},
				},
			},
		},
		{
			desc:      "remove acl on no switches",
			aclUUID:   "FAKE-UUID",
			expectErr: true,
			initialNbdb: libovsdbtest.TestSetup{
				NBData: []libovsdbtest.TestData{},
			},
			expectedNbdb: libovsdbtest.TestSetup{
				NBData: []libovsdbtest.TestData{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			var fakeModelClient ModelClient
			stopChan := make(chan struct{})

			nbClient, _ := libovsdbtest.NewNBTestHarness(tt.initialNbdb, stopChan)

			fakeSwitches := []nbdb.LogicalSwitch{
				*fakeSwitch1,
				*fakeSwitch2,
			}

			err := removeACLFromSwitches(nbClient, fakeSwitches, tt.aclUUID)
			if err != nil && !tt.expectErr {
				t.Fatal(fmt.Errorf("RemoveACLFromNodeSwitches() error = %v", err))
			}

			matcher := libovsdbtest.HaveDataIgnoringUUIDs(tt.expectedNbdb.NBData)
			success, err := matcher.Match(fakeModelClient.client)

			if !success {
				t.Fatal(fmt.Errorf("test: \"%s\" didn't match expected with actual, err: %v", tt.desc, matcher.FailureMessage(fakeModelClient.client)))
			}
			if err != nil {
				t.Fatal(fmt.Errorf("test: \"%s\" encountered error: %v", tt.desc, err))
			}

			close(stopChan)
		})
	}
}
