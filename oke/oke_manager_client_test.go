package oke

import (
	"github.com/rancher-plugins/kontainer-engine-driver-oke/oke/fakes"
	"github.com/rancher/kontainer-engine/types"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
	"testing"
)

var state State

func init() {
	info := types.ClusterInfo{}
	state, _ = GetState(&info)
	state.Name = "test-cluster"
	state.Network.VCNName = "test-vcn"
	state.CompartmentID = "ocid1.compartment.oc1..aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	state.Network.VcnCompartmentID = "ocid1.compartment.oc1..aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	state.NodePool.QuantityPerSubnet = 1
}

func TestCreateSubnets(t *testing.T) {

	oke, err := constructFakeClusterManagerClient()
	assert.Nil(t, err)
	assert.NotNil(t, oke)

	publicSubnet, err := oke.CreateSubnetWithDetails(nil, nil, nil, nil, nil, nil, false, nil, &state)
	assert.Nil(t, err)
	assert.NotNil(t, publicSubnet)
	assert.False(t, *publicSubnet.ProhibitPublicIpOnVnic)

	privateSubnet, err := oke.CreateSubnetWithDetails(nil, nil, nil, nil, nil, nil, true, nil, &state)
	assert.Nil(t, err)
	assert.NotNil(t, privateSubnet)
	assert.True(t, *privateSubnet.ProhibitPublicIpOnVnic)
}

func TestVCNCRUD(t *testing.T) {
	oke, err := constructFakeClusterManagerClient()
	assert.Nil(t, err)
	assert.NotNil(t, oke)

	vcnID, serviceSubnetIDs, nodeSubnetIds, err := oke.CreateVCNAndNetworkResources(&state)
	assert.Nil(t, err)
	assert.NotNil(t, vcnID)
	assert.True(t, len(nodeSubnetIds) == 3)
	assert.True(t, len(serviceSubnetIDs) == 2)

	// Spot check looking up VCN and subnets by name

	vcnID, err = oke.GetVcnIDByName(context.Background(), state.Network.VcnCompartmentID, state.Network.VCNName)
	assert.Nil(t, err)
	assert.NotNil(t, vcnID)

	subnetIDN1, err := oke.GetSubnetIDByName(context.Background(), state.Network.VcnCompartmentID, vcnID, node1SubnetName)
	assert.Nil(t, err)
	assert.True(t, len(subnetIDN1) > 0)

	subnetIDS2, err := oke.GetSubnetByName(context.Background(), state.Network.VcnCompartmentID, vcnID, service2SubnetName)
	assert.Nil(t, err)
	assert.NotNil(t, subnetIDS2.Id)
	assert.True(t, len(*subnetIDS2.Id) > 0)

	_, err = oke.GetVcnIDByName(context.Background(), state.Network.VcnCompartmentID, "DNE")
	assert.NotNil(t, err)

	_, err = oke.GetSubnetIDByName(context.Background(), state.Network.VcnCompartmentID, vcnID, "DNE")
	assert.NotNil(t, err)

	// There should be 5 subnets (3 for the nodes, 2 for the services)
	subnetIds, err := oke.ListSubnetIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(subnetIds) == 5)

	// There should be 1 IG and at least 2 security lists
	routeTableIds, err := oke.ListRouteTableIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(routeTableIds) == 1)

	internetGatewayIds, err := oke.ListInternetGatewayIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(internetGatewayIds) >= 1)

	securityListIds, err := oke.ListSecurityListIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(securityListIds) >= 2)

	err = oke.DeleteVCN(context.Background(), vcnID)
	assert.Nil(t, err)

	// There should be no more resources under this subnet
	subnetIds, err = oke.ListSubnetIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(subnetIds) == 0)
	internetGatewayIds, err = oke.ListInternetGatewayIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(internetGatewayIds) == 0)

	// Looking up a deleted subnet should result in an error
	vcnID, err = oke.GetVcnIDByName(context.Background(), state.Network.VcnCompartmentID, state.Network.VCNName)
	assert.NotNil(t, err)
}

func TestClusterCRUD(t *testing.T) {
	oke, err := constructFakeClusterManagerClient()
	assert.Nil(t, err)
	assert.NotNil(t, oke)

	defaultKubernetesVersion, err := getDefaultKubernetesVersion(oke.containerEngineClient)
	assert.Nil(t, err)
	assert.True(t, *defaultKubernetesVersion != "")

	vcnID, serviceSubnetIDs, nodeSubnetIds, err := oke.CreateVCNAndNetworkResources(&state)
	assert.Nil(t, err)
	assert.NotNil(t, vcnID)
	assert.True(t, len(nodeSubnetIds) > 0)
	assert.True(t, len(serviceSubnetIDs) > 0)

	err = oke.CreateCluster(context.Background(), &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	assert.Nil(t, err)
	assert.NotNil(t, state.ClusterID)

	cluster, err := oke.GetClusterByID(context.Background(), state.ClusterID)
	assert.Nil(t, err)
	assert.NotNil(t, cluster)
	assert.NotNil(t, *cluster.Id)
	assert.NotNil(t, *cluster.Name)
	assert.NotNil(t, *cluster.VcnId)
	assert.True(t, *cluster.KubernetesVersion == *defaultKubernetesVersion)
	assert.True(t, len(cluster.Options.ServiceLbSubnetIds) == 2)
	assert.True(t, len(*cluster.VcnId) > 0)

	err = oke.UpdateMasterKubernetesVersion(context.Background(), state.ClusterID, "v1.19.9")
	assert.Nil(t, err)
	cluster, err = oke.GetClusterByID(context.Background(), state.ClusterID)
	assert.Nil(t, err)
	assert.True(t, *cluster.KubernetesVersion == "v1.19.9")

	vcnID1, err := oke.GetVcnIDByClusterID(context.Background(), *cluster.Id)
	assert.Nil(t, err)
	assert.True(t, vcnID1 != "")
	assert.True(t, len(vcnID1) > 0)
	assert.Equal(t, vcnID1, vcnID)

	err = oke.DeleteCluster(context.Background(), *cluster.Id)
	assert.Nil(t, err)

	// Looking up a deleted cluster should result in an error
	cluster, err = oke.GetClusterByID(context.Background(), state.ClusterID)
	assert.NotNil(t, err)
	assert.Nil(t, cluster.Id)
}

func TestClusterCRUDWithNodePool(t *testing.T) {
	oke, err := constructFakeClusterManagerClient()
	assert.Nil(t, err)
	assert.NotNil(t, oke)

	defaultKubernetesVersion, err := getDefaultKubernetesVersion(oke.containerEngineClient)
	assert.Nil(t, err)
	assert.True(t, *defaultKubernetesVersion != "")

	vcnID, serviceSubnetIDs, nodeSubnetIds, err := oke.CreateVCNAndNetworkResources(&state)
	assert.Nil(t, err)
	assert.NotNil(t, vcnID)
	assert.True(t, len(nodeSubnetIds) > 0)
	assert.True(t, len(serviceSubnetIDs) > 0)

	err = oke.CreateCluster(context.Background(), &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	assert.Nil(t, err)
	assert.NotNil(t, state.ClusterID)

	clusterId, err := oke.GetClusterByName(context.Background(), state.CompartmentID, state.Name)
	assert.Nil(t, err)
	assert.NotNil(t, clusterId)

	_, err = oke.GetClusterByName(context.Background(), state.CompartmentID, "DNE")
	assert.NotNil(t, err)

	err = oke.CreateNodePool(context.Background(), &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	assert.Nil(t, err)

	nodePoolIDs, err := oke.ListNodepoolIdsInCluster(context.Background(), state.CompartmentID, state.ClusterID)
	assert.Nil(t, err)
	assert.True(t, len(nodePoolIDs) == 1)

	nodePool, err := oke.GetNodePoolByID(context.Background(), nodePoolIDs[0])

	assert.Nil(t, err)
	assert.NotNil(t, nodePool)
	assert.NotNil(t, *nodePool.Id)
	assert.NotNil(t, *nodePool.Name)
	assert.NotNil(t, *nodePool.ClusterId)
	assert.Equal(t, *nodePool.Id, nodePoolIDs[0])
	assert.Equal(t, *nodePool.ClusterId, clusterId)
	assert.True(t, int64(*nodePool.QuantityPerSubnet) == state.NodePool.QuantityPerSubnet)
	assert.True(t, *nodePool.KubernetesVersion == *defaultKubernetesVersion)

	err = oke.ScaleNodePool(context.Background(), nodePoolIDs[0], int(state.NodePool.QuantityPerSubnet)+1)
	assert.Nil(t, err)
	nodePool, _ = oke.GetNodePoolByID(context.Background(), nodePoolIDs[0])
	assert.Equal(t, *nodePool.Id, nodePoolIDs[0])
	assert.True(t, int64(*nodePool.QuantityPerSubnet) == state.NodePool.QuantityPerSubnet+1)

	err = oke.UpdateNodepoolKubernetesVersion(context.Background(), *nodePool.Id, "v1.19.9")
	assert.Nil(t, err)
	nodePool, _ = oke.GetNodePoolByID(context.Background(), nodePoolIDs[0])
	assert.Equal(t, *nodePool.Id, nodePoolIDs[0])
	assert.Nil(t, err)
	assert.True(t, *nodePool.KubernetesVersion == "v1.19.9")
	assert.True(t, int64(*nodePool.QuantityPerSubnet) == state.NodePool.QuantityPerSubnet+1)

	err = oke.DeleteNodePool(context.Background(), nodePoolIDs[0])
	assert.Nil(t, err)
	nodePoolIDs, err = oke.ListNodepoolIdsInCluster(context.Background(), state.CompartmentID, state.ClusterID)
	assert.Nil(t, err)
	assert.True(t, len(nodePoolIDs) == 0)

	err = oke.DeleteCluster(context.Background(), state.ClusterID)
	assert.Nil(t, err)

	// Looking up a deleted node pool should result in an error
	clusterId, err = oke.GetClusterByName(context.Background(), state.CompartmentID, state.Name)
	assert.NotNil(t, err)
	assert.True(t, clusterId == "")
}

func TestPrivateCluster(t *testing.T) {

	state.PrivateNodes = true

	oke, err := constructFakeClusterManagerClient()
	assert.Nil(t, err)
	assert.NotNil(t, oke)

	vcnID, serviceSubnetIDs, nodeSubnetIds, err := oke.CreateVCNAndNetworkResources(&state)
	assert.Nil(t, err)
	assert.NotNil(t, vcnID)
	assert.True(t, len(nodeSubnetIds) > 0)
	assert.True(t, len(serviceSubnetIDs) > 0)

	// There should be 6 subnets (3 for the nodes, 2 for the services, and 1 for the bastion)
	subnetIds, err := oke.ListSubnetIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(subnetIds) == 6)

	// A NAT Gateway should have been created when private nodes are enabled
	natGatewayIds, err := oke.ListNatGatewayIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(natGatewayIds) == 1)

	// And at least two route tables - one for the internet gateway and one for the NAT gateway
	routeTableIds, err := oke.ListRouteTableIdsInVcn(context.Background(), state.CompartmentID, vcnID)
	assert.Nil(t, err)
	assert.True(t, len(routeTableIds) >= 2)

	subnetNode1, err := oke.GetSubnetByName(context.Background(), state.Network.VcnCompartmentID, vcnID, node1SubnetName)
	assert.Nil(t, err)
	assert.NotNil(t, subnetNode1.RouteTableId)
	// Node has an explicit route for private workers.
	assert.True(t, len(*subnetNode1.RouteTableId) > 0)

	subnetService1, err := oke.GetSubnetByName(context.Background(), state.Network.VcnCompartmentID, vcnID, service1SubnetName)
	assert.Nil(t, err)
	// Default route can be null for service subnet or be the IG route, but always different than the node's NAT route.
	if subnetService1.RouteTableId != nil {
		assert.NotEqual(t, *subnetService1.RouteTableId, *subnetNode1.RouteTableId)
	}

	err = oke.CreateCluster(context.Background(), &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	assert.Nil(t, err)
	assert.NotNil(t, state.ClusterID)

	err = oke.CreateNodePool(context.Background(), &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	assert.Nil(t, err)

	nodePoolIDs, err := oke.ListNodepoolIdsInCluster(context.Background(), state.CompartmentID, state.ClusterID)
	assert.Nil(t, err)
	assert.True(t, len(nodePoolIDs) == 1)

	nodePool, err := oke.GetNodePoolByID(context.Background(), nodePoolIDs[0])

	assert.Nil(t, err)
	assert.NotNil(t, nodePool)
	assert.NotNil(t, *nodePool.Id)
	assert.NotNil(t, *nodePool.Name)
	assert.NotNil(t, *nodePool.ClusterId)
	assert.Equal(t, *nodePool.Id, nodePoolIDs[0])
	assert.True(t, int64(*nodePool.QuantityPerSubnet) == state.NodePool.QuantityPerSubnet)

	err = oke.DeleteNodePool(context.Background(), nodePoolIDs[0])
	assert.Nil(t, err)
	nodePoolIDs, err = oke.ListNodepoolIdsInCluster(context.Background(), state.CompartmentID, state.ClusterID)
	assert.Nil(t, err)
	assert.True(t, len(nodePoolIDs) == 0)
}

// newFakeClusterManagerClient creates a new OCI cluster manager, which has fake clients CE, VCN, Identity clients.
func newFakeClusterManagerClient() (*ClusterManagerClient, error) {

	containerClient, err := fakes.NewContainerEngineClient()
	if err != nil {
		logrus.Debugf("create new (fake) ContainerEngine client failed with err %v", err)
		return nil, err
	}
	vNetClient, err := fakes.NewVcnClient()
	if err != nil {
		logrus.Debugf("create new (fake) VirtualNetwork client failed with err %v", err)
		return nil, err
	}
	identityClient, err := fakes.NewIdentityClient()
	if err != nil {
		logrus.Debugf("create new (fake) Identity client failed with err %v", err)
		return nil, err
	}

	c := &ClusterManagerClient{
		containerEngineClient: containerClient,
		virtualNetworkClient:  vNetClient,
		identityClient:        identityClient,
		sleepDuration:         0,
	}

	return c, nil
}

// constructFakeClusterManagerClient is a helper function that constructs a new fake ClusterManagerClient.
func constructFakeClusterManagerClient() (ClusterManagerClient, error) {

	clusterMgrClient, err := newFakeClusterManagerClient()
	if err != nil {
		return ClusterManagerClient{}, err
	}

	return *clusterMgrClient, nil
}
