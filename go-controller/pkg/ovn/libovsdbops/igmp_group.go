package libovsdbops

import (
	"context"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// ListIGMPGroup checks to see if the server is using a schema version that
// includes the IGMP tabe
// NOTE: we shouldn't have to monitor this sbdb tables, since this function is only used to verify wether
// or not the server is aware of a certain table.  If any ovnkube functionality in the future
// requires actually using this, make sure to add explict monitoring of the `port_binding` table
// in github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/libovsdb.go
func CheckExistenceofIGMPGroup(sbClient libovsdbclient.Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	searchedIGMPGroup := []sbdb.IGMPGroup{}
	err := sbClient.List(ctx, &searchedIGMPGroup)
	if err != nil {
		klog.Errorf("Failed listing IGMP_Group err: %v", err)
		return false
	}

	return true
}
