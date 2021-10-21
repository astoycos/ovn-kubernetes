package libovsdbops

import (
	"fmt"
	"strings"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	libovsdb "github.com/ovn-org/libovsdb/ovsdb"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
)

// findSwitch looks up the switch in the cache and sets the UUID
func findSwitch(nbClient libovsdbclient.Client, lswitch *nbdb.LogicalSwitch) error {
	if lswitch.UUID != "" && !IsNamedUUID(lswitch.UUID) {
		return nil
	}

	switches := []nbdb.LogicalSwitch{}
	err := nbClient.WhereCache(func(item *nbdb.LogicalSwitch) bool {
		return item.Name == lswitch.Name
	}).List(&switches)
	if err != nil {
		return fmt.Errorf("can't find switch %+v: %v", *lswitch, err)
	}

	if len(switches) > 1 {
		return fmt.Errorf("unexpectedly found multiple switches: %+v", switches)
	}

	if len(switches) == 0 {
		return libovsdbclient.ErrNotFound
	}

	lswitch.UUID = switches[0].UUID
	return nil
}

// findSwitchesByPredicate Looks up switches in the cache based on the lookup function
func findSwitchesByPredicate(nbClient libovsdbclient.Client, lookupFunction func(item *nbdb.LogicalSwitch) bool) ([]nbdb.LogicalSwitch, error) {
	switches := []nbdb.LogicalSwitch{}
	err := nbClient.WhereCache(lookupFunction).List(&switches)
	if err != nil {
		return nil, fmt.Errorf("can't find switches: %v", err)
	}

	if len(switches) == 0 {
		return nil, libovsdbclient.ErrNotFound
	}

	return switches, nil
}

// FindSwitchesWithOtherConfig finds switches with otherconfig value/s
func FindSwitchesWithOtherConfig(nbClient libovsdbclient.Client) ([]nbdb.LogicalSwitch, error) {
	// Get all logical siwtches with other-config set
	otherConfigSearch := func(item *nbdb.LogicalSwitch) bool {
		return item.OtherConfig != nil
	}

	switches, err := findSwitchesByPredicate(nbClient, otherConfigSearch)
	if err != nil {
		return nil, err
	}

	return switches, nil
}

func AddLoadBalancersToSwitchOps(nbClient libovsdbclient.Client, ops []libovsdb.Operation, lswitch *nbdb.LogicalSwitch, lbs ...*nbdb.LoadBalancer) ([]libovsdb.Operation, error) {
	if ops == nil {
		ops = []libovsdb.Operation{}
	}
	if len(lbs) == 0 {
		return ops, nil
	}

	err := findSwitch(nbClient, lswitch)
	if err != nil {
		return nil, err
	}

	lbUUIDs := make([]string, 0, len(lbs))
	for _, lb := range lbs {
		lbUUIDs = append(lbUUIDs, lb.UUID)
	}

	op, err := nbClient.Where(lswitch).Mutate(lswitch, model.Mutation{
		Field:   &lswitch.LoadBalancer,
		Mutator: libovsdb.MutateOperationInsert,
		Value:   lbUUIDs,
	})
	if err != nil {
		return nil, err
	}
	ops = append(ops, op...)
	return ops, nil
}

func RemoveLoadBalancersFromSwitchOps(nbClient libovsdbclient.Client, ops []libovsdb.Operation, lswitch *nbdb.LogicalSwitch, lbs ...*nbdb.LoadBalancer) ([]libovsdb.Operation, error) {
	if ops == nil {
		ops = []libovsdb.Operation{}
	}
	if len(lbs) == 0 {
		return ops, nil
	}

	err := findSwitch(nbClient, lswitch)
	if err != nil {
		return nil, err
	}

	lbUUIDs := make([]string, 0, len(lbs))
	for _, lb := range lbs {
		lbUUIDs = append(lbUUIDs, lb.UUID)
	}

	op, err := nbClient.Where(lswitch).Mutate(lswitch, model.Mutation{
		Field:   &lswitch.LoadBalancer,
		Mutator: libovsdb.MutateOperationDelete,
		Value:   lbUUIDs,
	})
	if err != nil {
		return nil, err
	}
	ops = append(ops, op...)

	return ops, nil
}

func ListSwitchesWithLoadBalancers(nbClient libovsdbclient.Client) ([]nbdb.LogicalSwitch, error) {
	switches := &[]nbdb.LogicalSwitch{}
	err := nbClient.WhereCache(func(item *nbdb.LogicalSwitch) bool {
		return item.LoadBalancer != nil
	}).List(switches)
	return *switches, err
}

// RemoveACLFromSwitches removes the ACL uuid entry from Logical Switch acl's list.
func removeACLFromSwitches(nbClient libovsdbclient.Client, switches []nbdb.LogicalSwitch, aclUUID string) error {
	var opModels []OperationModel
	for i, sw := range switches {
		sw.ACLs = []string{aclUUID}
		swName := switches[i].Name
		opModels = append(opModels, OperationModel{
			Model:          &sw,
			ModelPredicate: func(ls *nbdb.LogicalSwitch) bool { return ls.Name == swName },
			OnModelMutations: []interface{}{
				&sw.ACLs,
			},
			ErrNotFound: true,
			BulkOp:      true,
		})
	}

	m := NewModelClient(nbClient)
	if err := m.Delete(opModels...); err != nil {
		return fmt.Errorf("error while removing ACL: %s, from switches err: %v", aclUUID, err)
	}

	return nil
}

// RemoveACLFromSwitches removes the ACL uuid entry from Logical Switch acl's list.
func RemoveACLFromNodeSwitches(nbClient libovsdbclient.Client, aclUUID string) error {
	// Find all node switches
	nodeSwichLookupFcn := func(item *nbdb.LogicalSwitch) bool {
		// Ignore external and Join switches
		if strings.Contains(item.Name, "join") || strings.Contains(item.Name, "ext") {
			return false
		}

		return false
	}

	switches, err := findSwitchesByPredicate(nbClient, nodeSwichLookupFcn)
	if err != nil {
		return err
	}

	err = removeACLFromSwitches(nbClient, switches, aclUUID)
	if err != nil {
		return err
	}

	return nil
}

// RemoveACLFromSwitches removes the ACL uuid entry from Logical Switch acl's list.
func RemoveACLFromAllSwitches(nbClient libovsdbclient.Client, aclUUID string) error {
	// Find all switches
	nodeSwichLookupFcn := func(item *nbdb.LogicalSwitch) bool {
		return true
	}

	switches, err := findSwitchesByPredicate(nbClient, nodeSwichLookupFcn)
	if err != nil {
		return err
	}

	err = removeACLFromSwitches(nbClient, switches, aclUUID)
	if err != nil {
		return err
	}

	return nil
}
