package libovsdbops

import (
	"context"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// CheckExistenceofPBUPColumn checks to see if the server is using a schema version that
// includes the `up` column within the port_binding table
// NOTE: we shouldn't have to monitor these sbdb tables, since this function is only used to verify wether
// or not the server is aware of a certain column.  If any ovnkube functionality in the future
// requires actually using this, make sure to add explict monitoring of the `port_binding` table
// in github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/libovsdb.go
func CheckExistenceofPBUpColumn(sbClient libovsdbclient.Client) bool {
	portBindings := []sbdb.PortBinding{}

	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()

	err := sbClient.List(ctx, &portBindings)
	if err != nil {
		klog.Errorf("Unable to list sbdb port_bind table entries, err:%v", err)
		return false
	}

	return true
}
