package check

import (
	"fmt"
	"strings"

	"github.com/vmware/govmomi/vim25/mo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// CheckComputeClusterPermissions confirms that resources associated with the node maintain required privileges.
type CheckComputeClusterPermissions struct {
	computeClusters map[string]*mo.ClusterComputeResource
}

var _ NodeCheck = &CheckComputeClusterPermissions{}

func (c *CheckComputeClusterPermissions) Name() string {
	return "CheckComputeClusterPermissions"
}

func (c *CheckComputeClusterPermissions) StartCheck() error {
	c.computeClusters = make(map[string]*mo.ClusterComputeResource)
	return nil
}

func (c *CheckComputeClusterPermissions) checkComputeClusterPrivileges(ctx *CheckContext, vCenter *VCenter, vm *mo.VirtualMachine, readOnly bool) error {
	cluster, err := getComputeCluster(ctx, vCenter, vm.Runtime.Host.Reference())
	if err != nil {
		klog.Infof("compute cluster resource could not be obtained for %v", vm.Reference())
		return nil
	}

	if readOnly {
		// Having read only privilege is implied if we don't trigger the error above.
		klog.Infof("confirmed read-only permissions for %v", vm.Reference())
		return nil
	}

	if _, ok := c.computeClusters[cluster.Name]; ok {
		klog.Infof("privileges for compute cluster %v have already been checked", cluster.Name)
		return nil
	}
	c.computeClusters[cluster.Name] = cluster

	if err := comparePrivileges(ctx.Context, vCenter.Username, cluster.Reference(), vCenter.AuthManager, permissions[permissionCluster]); err != nil {
		return fmt.Errorf("missing privileges for compute cluster %s: %s", cluster.Name, err.Error())
	}
	return nil
}

func (c *CheckComputeClusterPermissions) CheckNode(ctx *CheckContext, node *v1.Node, vm *mo.VirtualMachine) error {
	var errs []error
	readOnly := false

	vCenterInfo, err := GetVCenter(ctx, node)
	if err != nil {
		return fmt.Errorf("unable to check node %s: %s", node.Name, err)
	}

	// If pre-existing resource pool was defined, only check cluster for read privilege.
	// Note: Older installs use legacy config.  Newer installs are using the yaml and this
	// field is not in there, so we fall back to using infrastructure.
	if ctx.VMConfig.LegacyConfig != nil && ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath != "" {
		klog.Info("Detected legacy config with custom ResourcePool")
		readOnly = true
	} else if ctx.PlatformSpec != nil {
		// Mirror GetVCenter's failure-domain selection so both agree on which FD owns
		// this node (OCPBUGS-63365):
		//  1. Read GA topology labels; fall back to the legacy beta labels.
		//  2. With exactly one FD, topology labels are not required — use it directly
		//     (same logic as GetVCenter / OCPBUGS-59319).
		//  3. With more than one FD, match by region+zone. Missing or unmatched labels
		//     indicate a mis-labeled node; log a warning and skip the readOnly shortcut.
		region := node.Labels[v1.LabelTopologyRegion]
		if len(region) == 0 {
			region = node.Labels[v1.LabelFailureDomainBetaRegion]
		}
		zone := node.Labels[v1.LabelTopologyZone]
		if len(zone) == 0 {
			zone = node.Labels[v1.LabelFailureDomainBetaZone]
		}

		var matchingRP string
		if len(ctx.PlatformSpec.FailureDomains) == 1 {
			matchingRP = ctx.PlatformSpec.FailureDomains[0].Topology.ResourcePool
		} else if len(region) == 0 || len(zone) == 0 {
			klog.Warningf("node %s has no topology labels in a multi-FD cluster; cannot determine failure domain for readOnly check", node.Name)
		} else {
			for _, fd := range ctx.PlatformSpec.FailureDomains {
				if fd.Region == region && fd.Zone == zone {
					matchingRP = fd.Topology.ResourcePool
					break
				}
			}
			if matchingRP == "" {
				klog.Warningf("node %s has topology labels region=%s zone=%s but no matching failure domain found; node may be mis-labeled", node.Name, region, zone)
			}
		}

		if matchingRP != "" && !strings.HasSuffix(matchingRP, "/Resources") {
			klog.Info("Detected failure domain with custom ResourcePool")
			readOnly = true
		}
	}
	klog.V(4).Infof("Is compute cluster read only? %v", readOnly)

	err = c.checkComputeClusterPrivileges(ctx, vCenterInfo, vm, readOnly)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return join(errs)
	}
	return nil
}

func (c *CheckComputeClusterPermissions) FinishCheck(ctx *CheckContext) {}
