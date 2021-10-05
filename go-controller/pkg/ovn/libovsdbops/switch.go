package libovsdbops

import (
	"fmt"
	"strings"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	libovsdb "github.com/ovn-org/libovsdb/ovsdb"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
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

func FindManagmentAndHoPortForNode(nbClient libovsdbclient.Client, nodeName string) ([]nbdb.LogicalSwitchPort, error) {
	lsps := &[]nbdb.LogicalSwitchPort{}
	err := nbClient.WhereCache(func(item *nbdb.LogicalSwitchPort) bool {
		return strings.Contains(item.Name, types.K8sPrefix+nodeName) ||
			strings.Contains(item.Name, types.HybridOverlayPrefix+nodeName)
	}).List(lsps)
	return *lsps, err
}

func SetSwitchOtherConfigOps(nbClient libovsdbclient.Client, ops []libovsdb.Operation, lswitch *nbdb.LogicalSwitch, key, value string) ([]libovsdb.Operation, error) {
	if ops == nil {
		ops = []libovsdb.Operation{}
	}

	err := findSwitch(nbClient, lswitch)
	if err != nil {
		return nil, err
	}

	otherConfig := map[string]string{
		key: value,
	}

	op, err := nbClient.Where(lswitch).Mutate(lswitch, model.Mutation{
		Field:   &lswitch.OtherConfig,
		Mutator: libovsdb.MutateOperationInsert,
		Value:   otherConfig,
	})
	if err != nil {
		return nil, err
	}
	ops = append(ops, op...)

	return ops, nil
}

func RemoveSwitchOtherConfigOps(nbClient libovsdbclient.Client, ops []libovsdb.Operation, lswitch *nbdb.LogicalSwitch, key string) ([]libovsdb.Operation, error) {
	if ops == nil {
		ops = []libovsdb.Operation{}
	}

	err := findSwitch(nbClient, lswitch)
	if err != nil {
		return nil, err
	}

	otherConfig := map[string]string{
		key: "",
	}

	op, err := nbClient.Where(lswitch).Mutate(lswitch, model.Mutation{
		Field:   &lswitch.OtherConfig,
		Mutator: libovsdb.MutateOperationDelete,
		Value:   otherConfig,
	})
	if err != nil {
		return nil, err
	}
	ops = append(ops, op...)

	return ops, nil
}
