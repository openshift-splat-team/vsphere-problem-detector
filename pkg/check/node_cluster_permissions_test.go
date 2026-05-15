package check

import (
	"context"
	"testing"

	"github.com/golang/mock/gomock"
	ocpv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/session"
	k8sv1 "k8s.io/api/core/v1"

	"github.com/openshift/vsphere-problem-detector/pkg/testlib"
)

// helper to build a missing-cluster-permissions AuthManager and attach it
func setMissingClusterPermissionsAuthManager(t *testing.T, ctx *CheckContext) {
	t.Helper()
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	// Use the first vCenter (dc0 in simulator configs)
	vcenter := ctx.VCenters["dc0"]
	finder := find.NewFinder(vcenter.VMClient)
	sessionMgr := session.NewManager(vcenter.VMClient)
	userSession, err := sessionMgr.UserSession(ctx.Context)
	if err != nil {
		t.Fatalf("failed to get user session: %v", err)
	}

	authMgr, err := buildAuthManagerClient(context.TODO(), mockCtrl, finder, userSession.UserName, &permissionCluster, []string{})
	if err != nil {
		t.Fatalf("failed to build mock auth manager: %v", err)
	}
	ctx.VCenters["dc0"].AuthManager = authMgr
}

func TestCheckComputeClusterPermissions_LegacyINI_ReadOnly(t *testing.T) {
	// Stage
	check := &CheckComputeClusterPermissions{}
	if err := check.StartCheck(); err != nil {
		t.Fatalf("StartCheck failed: %v", err)
	}

	node := testlib.Node("DC0_H0_VM0")
	kubeClient := &testlib.FakeKubeClient{
		Infrastructure: testlib.Infrastructure(), // legacy infra shape is fine
		Nodes:          []*k8sv1.Node{node},
	}
	ctx, cleanup, err := SetupSimulator(kubeClient, testlib.DefaultModel)
	if err != nil {
		t.Fatalf("setupSimulator failed: %s", err)
	}
	defer cleanup()

	// Force legacy path: non-empty ResourcePoolPath implies read-only cluster check
	if ctx.VMConfig.LegacyConfig == nil {
		t.Fatalf("expected legacy config to be present in simulator setup")
	}
	ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath = "/DC0/host/DC0_C0/Resources/custom"

	// Ensure cluster permissions would be missing if checked (to prove read_only skips)
	setMissingClusterPermissionsAuthManager(t, ctx)

	// Get VM for node
	vCenter, err := GetVCenter(ctx, node)
	if err != nil {
		t.Fatalf("error getting vCenter for node %s: %s", node.Name, err)
	}
	vm, err := testlib.GetVM(vCenter.VMClient, node)
	if err != nil {
		t.Fatalf("error getting vm for node %s: %s", node.Name, err)
	}

	// Act
	err = check.CheckNode(ctx, node, vm)

	// Assert: should NOT error due to read-only path
	assert.NoError(t, err)
}

func TestCheckComputeClusterPermissions_Infrastructure_ReadOnly_WithCustomRP(t *testing.T) {
	// Stage
	check := &CheckComputeClusterPermissions{}
	if err := check.StartCheck(); err != nil {
		t.Fatalf("StartCheck failed: %v", err)
	}

	// Node with region/zone labels matching the failure domain
	node := testlib.Node("DC0_H0_VM0", func(n *k8sv1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels["topology.kubernetes.io/region"] = "east"
		n.Labels["topology.kubernetes.io/zone"] = "east-1a"
	})

	infra := testlib.InfrastructureWithFailureDomain(func(inf *ocpv1.Infrastructure) {
		// Set a custom ResourcePool path (not ending with /Resources) to trigger read_only
		inf.Spec.PlatformSpec.VSphere.FailureDomains[0].Topology.ResourcePool = "/DC0/host/DC0_C0/Resources/test-resourcepool"
	})

	kubeClient := &testlib.FakeKubeClient{
		Infrastructure: infra,
		Nodes:          []*k8sv1.Node{node},
	}
	ctx, cleanup, err := SetupSimulator(kubeClient, testlib.DefaultModel)
	if err != nil {
		t.Fatalf("setupSimulator failed: %s", err)
	}
	defer cleanup()

	// Ensure legacy path does not interfere
	if ctx.VMConfig.LegacyConfig != nil {
		ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath = ""
	}
	// Ensure cluster permissions would be missing if checked (to prove infra read_only skips)
	setMissingClusterPermissionsAuthManager(t, ctx)

	// Get VM for node
	vCenter, err := GetVCenter(ctx, node)
	if err != nil {
		t.Fatalf("error getting vCenter for node %s: %s", node.Name, err)
	}
	vm, err := testlib.GetVM(vCenter.VMClient, node)
	if err != nil {
		t.Fatalf("error getting vm for node %s: %s", node.Name, err)
	}

	// Act
	err = check.CheckNode(ctx, node, vm)

	// Assert: should NOT error due to infra-based read-only
	assert.NoError(t, err)
}

// TestCheckComputeClusterPermissions_BetaLabels_CustomRP_ReadOnly reproduces gap #1:
// when a node carries the deprecated failure-domain.beta.kubernetes.io labels,
// CheckNode must still match the failure domain and set readOnly=true.
// Two failure domains are used so the multi-FD code path (which does region/zone
// matching) is exercised rather than the single-FD fallback.
func TestCheckComputeClusterPermissions_BetaLabels_CustomRP_ReadOnly(t *testing.T) {
	check := &CheckComputeClusterPermissions{}
	if err := check.StartCheck(); err != nil {
		t.Fatalf("StartCheck failed: %v", err)
	}

	// Node carries deprecated beta labels (v1.LabelFailureDomainBetaRegion /
	// v1.LabelFailureDomainBetaZone = "failure-domain.beta.kubernetes.io/region/zone").
	node := testlib.Node("DC0_C0_RP0_VM0", func(n *k8sv1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels[k8sv1.LabelFailureDomainBetaRegion] = "east"
		n.Labels[k8sv1.LabelFailureDomainBetaZone] = "east-1a"
	})

	// Two failure domains — ensures len(FDs) > 1 so the multi-FD matching path
	// is exercised and beta-label fallback is actually needed.
	infra := testlib.InfrastructureWithFailureDomain(func(inf *ocpv1.Infrastructure) {
		// First FD: matches the node's beta labels, has a custom resource pool.
		inf.Spec.PlatformSpec.VSphere.FailureDomains[0].Topology.ResourcePool = "/DC0/host/DC0_C0/Resources/test-resourcepool"
		// Second FD: different region/zone, no custom RP.
		inf.Spec.PlatformSpec.VSphere.FailureDomains = append(
			inf.Spec.PlatformSpec.VSphere.FailureDomains,
			ocpv1.VSpherePlatformFailureDomainSpec{
				Name:   "west",
				Region: "west",
				Zone:   "west-1a",
				Server: "dc0",
				Topology: ocpv1.VSpherePlatformTopology{
					Datacenter: "DC0",
					Datastore:  "LocalDS_0",
				},
			},
		)
	})

	kubeClient := &testlib.FakeKubeClient{
		Infrastructure: infra,
		Nodes:          []*k8sv1.Node{node},
	}
	ctx, cleanup, err := SetupSimulator(kubeClient, testlib.DefaultModel)
	if err != nil {
		t.Fatalf("setupSimulator failed: %s", err)
	}
	defer cleanup()

	if ctx.VMConfig.LegacyConfig != nil {
		ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath = ""
	}
	// Missing cluster permissions: the check should NOT reach this path (readOnly=true).
	setMissingClusterPermissionsAuthManager(t, ctx)

	vCenter, err := GetVCenter(ctx, node)
	if err != nil {
		t.Fatalf("error getting vCenter for node %s: %s", node.Name, err)
	}
	vm, err := testlib.GetVM(vCenter.VMClient, node)
	if err != nil {
		t.Fatalf("error getting vm for node %s: %s", node.Name, err)
	}

	// Without the fix: beta labels are not read, so readOnly stays false, and the
	// cluster permission check fires — "missing privileges for compute cluster DC0_C0".
	// With the fix: beta labels are read, east FD is matched, readOnly=true.
	err = check.CheckNode(ctx, node, vm)
	assert.NoError(t, err, "expected no error: custom RP found via beta-label match should set readOnly=true")
}

// TestCheckComputeClusterPermissions_SingleFD_NoLabels_CustomRP reproduces gap #2:
// GetVCenter falls back to the first failure domain when the cluster has only one FD
// (OCPBUGS-59319).  CheckNode must apply the same fallback when determining readOnly,
// otherwise nodes without topology labels always trigger a full cluster-permission check.
func TestCheckComputeClusterPermissions_SingleFD_NoLabels_CustomRP(t *testing.T) {
	check := &CheckComputeClusterPermissions{}
	if err := check.StartCheck(); err != nil {
		t.Fatalf("StartCheck failed: %v", err)
	}

	// Node has NO topology labels — mirrors a newly-provisioned worker before
	// the node controller has had a chance to apply them.
	node := testlib.Node("DC0_C0_RP0_VM0")

	// Single failure domain with a custom resource pool (not the default /Resources).
	infra := testlib.InfrastructureWithFailureDomain(func(inf *ocpv1.Infrastructure) {
		inf.Spec.PlatformSpec.VSphere.FailureDomains[0].Topology.ResourcePool = "/DC0/host/DC0_C0/Resources/test-resourcepool"
	})

	kubeClient := &testlib.FakeKubeClient{
		Infrastructure: infra,
		Nodes:          []*k8sv1.Node{node},
	}
	ctx, cleanup, err := SetupSimulator(kubeClient, testlib.DefaultModel)
	if err != nil {
		t.Fatalf("setupSimulator failed: %s", err)
	}
	defer cleanup()

	if ctx.VMConfig.LegacyConfig != nil {
		ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath = ""
	}
	// Missing cluster permissions: the check should NOT reach this path (readOnly=true).
	setMissingClusterPermissionsAuthManager(t, ctx)

	vCenter, err := GetVCenter(ctx, node)
	if err != nil {
		t.Fatalf("error getting vCenter for node %s: %s", node.Name, err)
	}
	vm, err := testlib.GetVM(vCenter.VMClient, node)
	if err != nil {
		t.Fatalf("error getting vm for node %s: %s", node.Name, err)
	}

	// Bug: the single-FD fallback is missing, so the region/zone match fails for a
	// node without labels, readOnly stays false, and the cluster permission check fires.
	// After the fix this must succeed.
	err = check.CheckNode(ctx, node, vm)
	assert.NoError(t, err, "expected no error: single FD with custom RP should make this a read-only cluster check")
}

// TestCheckComputeClusterPermissions_MultiFD_NoLabels_RequiresClusterPermissions locks in
// the negative behavior: when there are multiple failure domains and the node has no
// topology labels, CheckNode must NOT fall back to the first FD. readOnly stays false
// and the full cluster-permission check runs — surfacing missing privileges.
func TestCheckComputeClusterPermissions_MultiFD_NoLabels_RequiresClusterPermissions(t *testing.T) {
	check := &CheckComputeClusterPermissions{}
	if err := check.StartCheck(); err != nil {
		t.Fatalf("StartCheck failed: %v", err)
	}

	// Node has NO topology labels in a multi-FD cluster.
	node := testlib.Node("DC0_C0_RP0_VM0")

	// Two failure domains — first has a custom RP, which would set readOnly=true if
	// incorrectly selected via a first-FD fallback.
	infra := testlib.InfrastructureWithFailureDomain(func(inf *ocpv1.Infrastructure) {
		inf.Spec.PlatformSpec.VSphere.FailureDomains[0].Topology.ResourcePool = "/DC0/host/DC0_C0/Resources/test-resourcepool"
		inf.Spec.PlatformSpec.VSphere.FailureDomains = append(
			inf.Spec.PlatformSpec.VSphere.FailureDomains,
			ocpv1.VSpherePlatformFailureDomainSpec{
				Name:   "west",
				Region: "west",
				Zone:   "west-1a",
				Server: "dc0",
				Topology: ocpv1.VSpherePlatformTopology{
					Datacenter: "DC0",
					Datastore:  "LocalDS_0",
				},
			},
		)
	})

	kubeClient := &testlib.FakeKubeClient{
		Infrastructure: infra,
		Nodes:          []*k8sv1.Node{node},
	}
	ctx, cleanup, err := SetupSimulator(kubeClient, testlib.DefaultModel)
	if err != nil {
		t.Fatalf("setupSimulator failed: %s", err)
	}
	defer cleanup()

	if ctx.VMConfig.LegacyConfig != nil {
		ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath = ""
	}
	// Simulate missing cluster permissions — must be reached because readOnly=false.
	setMissingClusterPermissionsAuthManager(t, ctx)

	vCenter, err := GetVCenter(ctx, node)
	if err != nil {
		t.Fatalf("error getting vCenter for node %s: %s", node.Name, err)
	}
	vm, err := testlib.GetVM(vCenter.VMClient, node)
	if err != nil {
		t.Fatalf("error getting vm for node %s: %s", node.Name, err)
	}

	// With no labels in a multi-FD cluster, no FD is matched and readOnly stays false.
	// The cluster permission check must fire and return an error.
	err = check.CheckNode(ctx, node, vm)
	assert.Error(t, err, "expected error: multi-FD cluster with no node labels should not skip cluster permission check")
	assert.Contains(t, err.Error(), "missing privileges for compute cluster")
}

func TestCheckComputeClusterPermissions_Infrastructure_NotReadOnly_DefaultRP(t *testing.T) {
	// Stage
	check := &CheckComputeClusterPermissions{}
	if err := check.StartCheck(); err != nil {
		t.Fatalf("StartCheck failed: %v", err)
	}

	// Node with region/zone labels matching the failure domain
	node := testlib.Node("DC0_H0_VM0", func(n *k8sv1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels["topology.kubernetes.io/region"] = "east"
		n.Labels["topology.kubernetes.io/zone"] = "east-1a"
	})

	infra := testlib.InfrastructureWithFailureDomain(func(inf *ocpv1.Infrastructure) {
		// Set ResourcePool to default cluster Resources (ends with /Resources) -> not read-only
		inf.Spec.PlatformSpec.VSphere.FailureDomains[0].Topology.ResourcePool = "/DC0/host/DC0_C0/Resources"
	})

	kubeClient := &testlib.FakeKubeClient{
		Infrastructure: infra,
		Nodes:          []*k8sv1.Node{node},
	}
	ctx, cleanup, err := SetupSimulator(kubeClient, testlib.DefaultModel)
	if err != nil {
		t.Fatalf("setupSimulator failed: %s", err)
	}
	defer cleanup()

	// Ensure legacy path does not interfere
	if ctx.VMConfig.LegacyConfig != nil {
		ctx.VMConfig.LegacyConfig.Workspace.ResourcePoolPath = ""
	}
	// Force missing cluster permissions so the non-read-only path surfaces an error
	setMissingClusterPermissionsAuthManager(t, ctx)

	// Get VM for node
	vCenter, err := GetVCenter(ctx, node)
	if err != nil {
		t.Fatalf("error getting vCenter for node %s: %s", node.Name, err)
	}
	vm, err := testlib.GetVM(vCenter.VMClient, node)
	if err != nil {
		t.Fatalf("error getting vm for node %s: %s", node.Name, err)
	}

	// Act
	err = check.CheckNode(ctx, node, vm)

	// Assert: expect no error when using custom ResourcePool
	assert.NoError(t, err)
}
