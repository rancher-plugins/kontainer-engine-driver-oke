// Copyright 2019 Oracle and/or its affiliates. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package oke

/*
ClusterManagerClient is a client for interacting with Oracle Cloud Engine API.
It does all of the real work in providing CRUD operations for the OKE cluster
and VCN.
*/
import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/oracle/oci-go-sdk/v38/common"
	"github.com/oracle/oci-go-sdk/v38/containerengine"
	"github.com/oracle/oci-go-sdk/v38/core"
	"github.com/oracle/oci-go-sdk/v38/example/helpers"
	"github.com/oracle/oci-go-sdk/v38/identity"
	"github.com/rancher/kontainer-engine/store"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v2"
)

const (
	// TODO VCN block only needs to be large enough for the subnets below
	vcnCIDRBlock                       = "10.0.0.0/16"
	controlPlaneCIDRBlock              = "10.0.0.0/28"
	nodeCIDRBlock                      = "10.0.10.0/24"
	bastionCIDRBlock                   = "10.0.16.0/24"
	serviceCIDRBlock                   = "10.0.20.0/24"
	podCIDRBlock                       = "10.244.0.0/16"
	servicesCIDRBlock                  = "10.96.0.0/16"
	controlPlaneSubnetName             = "k8sendpoint"
	nodeSubnetName                     = "nodedns"
	serviceSubnetName                  = "svclbsubnet"
	bastionSubnetName                  = "bastionsubnet"
	controlPlaneSubnetSecurityListName = "oke-k8sapiendpointseclist"
	nodePoolSubnetSecurityListName     = "oke-nodeseclist"
	serviceSubnetSecurityListName      = "oke-svclbseclist"
)

// Defines / contains the OCI/OKE/Identity clients and operations.
type ClusterManagerClient struct {
	configuration         common.ConfigurationProvider
	containerEngineClient containerengine.ContainerEngineClient
	computeClient         core.ComputeClient
	virtualNetworkClient  core.VirtualNetworkClient
	identityClient        identity.IdentityClient
	sleepDuration         time.Duration
	// TODO we could also include the retry settings here
}

// NewClusterManagerClient creates a new OCI cluster manager, which has a set of
// clients (CE, VCN, Identity).
func NewClusterManagerClient(configuration common.ConfigurationProvider) (*ClusterManagerClient, error) {

	containerClient, err := containerengine.NewContainerEngineClientWithConfigurationProvider(configuration)
	if err != nil {
		logrus.Debugf("create new ContainerEngine client failed with err %v", err)
		return nil, err
	}
	coreComputeClient, err := core.NewComputeClientWithConfigurationProvider(configuration)
	if err != nil {
		logrus.Debugf("create new Compute client failed with err %v", err)
		return nil, err
	}
	vNetClient, err := core.NewVirtualNetworkClientWithConfigurationProvider(configuration)
	if err != nil {
		logrus.Debugf("create new VirtualNetwork client failed with err %v", err)
		return nil, err
	}
	identityClient, err := identity.NewIdentityClientWithConfigurationProvider(configuration)
	if err != nil {
		logrus.Debugf("create new Identity client failed with err %v", err)
		return nil, err
	}
	c := &ClusterManagerClient{
		configuration:         configuration,
		containerEngineClient: containerClient,
		computeClient:         coreComputeClient,
		virtualNetworkClient:  vNetClient,
		identityClient:        identityClient,
		sleepDuration:         5,
	}
	return c, nil
}

// CreateCluster creates a new cluster with no initial node pool and attaches
// it to the existing network resources, or an error.
// TODO stop passing in state
func (mgr *ClusterManagerClient) CreateCluster(ctx context.Context, state *State, vcnID string, controlPlaneSubnetID string, serviceSubnetIds, nodeSubnetIds []string) error {
	if state == nil {
		return fmt.Errorf("valid state is required")
	}
	logrus.Debugf("creating cluster %s with VCN ID %s", state.Name, vcnID)

	if state.KubernetesVersion == "" {
		kubernetesVersion, err := getDefaultKubernetesVersion(mgr.containerEngineClient)
		if err != nil {
			return err
		} else if kubernetesVersion == nil {
			return fmt.Errorf("could not determine default Kubernetes version")
		}
		state.KubernetesVersion = *kubernetesVersion
	}

	cReq := containerengine.CreateClusterRequest{}
	cReq.Name = common.String(state.Name)
	cReq.CompartmentId = &state.CompartmentID
	cReq.VcnId = common.String(vcnID)
	cReq.KubernetesVersion = common.String(state.KubernetesVersion)
	cReq.Options = &containerengine.ClusterCreateOptions{
		ServiceLbSubnetIds: serviceSubnetIds,
		AddOns: &containerengine.AddOnOptions{
			IsKubernetesDashboardEnabled: common.Bool(state.EnableKubernetesDashboard),
			IsTillerEnabled:              common.Bool(state.EnableTiller),
		},
		KubernetesNetworkConfig: &containerengine.KubernetesNetworkConfig{
			PodsCidr:     common.String(state.Network.PodCidr),
			ServicesCidr: common.String(state.Network.ServiceCidr),
		},
	}
	if len(controlPlaneSubnetID) > 0 {
		cReq.EndpointConfig = &containerengine.CreateClusterEndpointConfigDetails{
			SubnetId:          common.String(controlPlaneSubnetID),
			IsPublicIpEnabled: negate(state.PrivateControlPlane),
		}
	}

	clusterResp, err := mgr.containerEngineClient.CreateCluster(ctx, cReq)
	if err != nil {
		return err
	}

	// wait until cluster creation work request complete
	logrus.Debugf("waiting for cluster %s to reach Active status..", state.Name)
	// initial delay since subsequent back-off function waits longer each time the retry fails
	time.Sleep(time.Minute * 3)
	workReqRespCluster, err := waitUntilWorkRequestComplete(mgr.containerEngineClient, clusterResp.OpcWorkRequestId)
	if err != nil {
		logrus.Debugf("get work request failed with err %v", err)
		return err
	}

	clusterID := getResourceID(workReqRespCluster.Resources, containerengine.WorkRequestResourceActionTypeCreated, string(containerengine.ListWorkRequestsResourceTypeCluster))

	if clusterID == nil {
		return fmt.Errorf("could not retrieve clusterID")
	}

	logrus.Debugf("clusterID: %s has been created", *clusterID)
	state.ClusterID = *clusterID

	return nil
}

// GetClusterByID returns the cluster with the specified Id, or an error
func (mgr *ClusterManagerClient) GetClusterByID(ctx context.Context, clusterID string) (containerengine.Cluster, error) {

	logrus.Debugf("getting cluster with Cluster ID %s", clusterID)

	if len(clusterID) == 0 {
		return containerengine.Cluster{}, fmt.Errorf("clusterID must be set to retrieve the cluster")
	}

	req := containerengine.GetClusterRequest{}
	req.ClusterId = common.String(clusterID)

	resp, err := mgr.containerEngineClient.GetCluster(ctx, req)
	if err != nil {
		logrus.Debugf("get cluster request failed with err %v", err)
		return containerengine.Cluster{}, err
	}

	return resp.Cluster, nil
}

// GetClusterByName returns the Cluster ID of the Cluster with the specified
// name in the specified compartment or an error if it is not found.
func (mgr *ClusterManagerClient) GetClusterByName(ctx context.Context, compartmentID, name string) (string, error) {
	logrus.Debugf("getting cluster with name %s", name)

	if len(compartmentID) == 0 {
		return "", fmt.Errorf("compartmentID must be set to retrieve the cluster")
	} else if len(name) == 0 {
		return "", fmt.Errorf("name must be set to retrieve the cluster")
	}

	listClustersReq := containerengine.ListClustersRequest{}
	listClustersReq.CompartmentId = common.String(compartmentID)
	listClustersReq.Name = common.String(name)

	listClustersResp, err := mgr.containerEngineClient.ListClusters(ctx, listClustersReq)
	if err != nil {
		logrus.Debugf("list clusters failed with err %v", err)
		return "", err
	}
	for _, cluster := range listClustersResp.Items {
		if *cluster.Name == name {
			return *cluster.Id, nil
		}
	}

	return "", fmt.Errorf("%s not found", name)
}

// CreateNodePool creates a new node pool (i.e. a set of compute nodes) for the
// cluster, or an error.
// TODO stop passing in state
func (mgr *ClusterManagerClient) CreateNodePool(ctx context.Context, state *State, vcnID string, serviceSubnetIds, nodeSubnetIds []string) error {
	if state == nil {
		return fmt.Errorf("valid state is required")
	}
	logrus.Debugf("creating node pool %s with VCN ID %s", state.Name, vcnID)

	if state.KubernetesVersion == "" {
		kubernetesVersion, err := getDefaultKubernetesVersion(mgr.containerEngineClient)
		if err != nil {
			return err
		}
		state.KubernetesVersion = *kubernetesVersion
	}

	// Create a node pool for the cluster
	npReq := containerengine.CreateNodePoolRequest{}
	// get Image Id
	image, err := getImageID(ctx, mgr.computeClient, state.CompartmentID, state.NodePool.NodeShape, state.NodePool.NodeImageName)
	if err != nil {
		logrus.Printf("Node image ID not found")
		return err
	} else {
		logrus.Printf("Node image ID found %v", *image.Id)
		// Set a custom boot volume size if set
		if state.NodePool.CustomBootVolumeSize != 0 {
			npReq.NodeSourceDetails = containerengine.NodeSourceViaImageDetails{ImageId: image.Id,
				BootVolumeSizeInGBs: common.Int64(state.NodePool.CustomBootVolumeSize)}
		} else {
			npReq.NodeSourceDetails = containerengine.NodeSourceViaImageDetails{ImageId: image.Id}
		}
	}

	req := identity.ListAvailabilityDomainsRequest{}
	req.CompartmentId = &state.CompartmentID
	allADs, err := mgr.identityClient.ListAvailabilityDomains(ctx, req)
	if err != nil {
		return err
	}

	// Our particular image may not be available in every available AD
	usableADs := make([]identity.AvailabilityDomain, 0, len(allADs.Items))

	// Only add the AD to usableADs if our image is available.
	for i := 0; i < len(allADs.Items); i++ {
		listShapesReq := core.ListShapesRequest{}
		listShapesReq.ImageId = image.Id
		listShapesReq.CompartmentId = common.String(state.CompartmentID)
		listShapesReq.AvailabilityDomain = allADs.Items[i].Name
		listShapes, err := mgr.computeClient.ListShapes(ctx, listShapesReq)
		if err != nil {
			return err
		}
		for _, shape := range listShapes.Items {
			if *shape.Shape == state.NodePool.NodeShape {
				usableADs = append(usableADs, allADs.Items[i])
			}
		}
	}

	npReq.Name = common.String(state.Name + "-1")
	npReq.CompartmentId = common.String(state.CompartmentID)
	npReq.ClusterId = &state.ClusterID
	npReq.KubernetesVersion = &state.KubernetesVersion
	npReq.NodeShape = common.String(state.NodePool.NodeShape)
	if state.NodePool.FlexOCPUs != 0 {
		logrus.Debugf("creating node-pool with %d OCPUs", state.NodePool.FlexOCPUs)
		var ocpus = float32(state.NodePool.FlexOCPUs)
		npReq.NodeShapeConfig = &containerengine.CreateNodeShapeConfigDetails{Ocpus: &ocpus}
	}

	// Node-pool subnet(s) used for node instances in the node pool.
	// These subnets should be different from the cluster Kubernetes Service LB subnets.
	npReq.InitialNodeLabels = []containerengine.KeyValue{{Key: common.String("driver"), Value: common.String("oraclekubernetesengine")}}
	if state.NodePool.NodePublicSSHKeyContents != "" {
		npReq.SshPublicKey = common.String(state.NodePool.NodePublicSSHKeyContents)
	}
	npReq.NodeConfigDetails = &containerengine.CreateNodePoolNodeConfigDetails{
		PlacementConfigs: make([]containerengine.NodePoolPlacementConfigDetails, 0, len(usableADs)),
		Size:             common.Int(limitN(int(state.NodePool.QuantityPerSubnet)*len(usableADs), int(state.NodePool.LimitNodeCount))),
	}

	// Match up subnet(s) to availability domains
	for i := 0; i < len(usableADs); i++ {
		nextSubnetId := ""
		if len(nodeSubnetIds) == 1 {
			// use a single regional subnet (default)
			nextSubnetId = nodeSubnetIds[0]
		} else {
			// use AD specific subnets (possibly coming from an existing VCN)
			nextSubnetId = nodeSubnetIds[i]
		}
		npReq.NodeConfigDetails.PlacementConfigs = append(npReq.NodeConfigDetails.PlacementConfigs,
			containerengine.NodePoolPlacementConfigDetails{
				AvailabilityDomain: usableADs[i].Name,
				SubnetId:           &nextSubnetId,
			})
	}

	createNodePoolResp, err := mgr.containerEngineClient.CreateNodePool(ctx, npReq)
	if err != nil {
		logrus.Debugf("create node pool request failed with err %v", err)
		return err
	}

	// wait until cluster creation work request complete
	logrus.Debugf("waiting for node pool to be created...")
	workReqRespNodePool, err := waitUntilWorkRequestComplete(mgr.containerEngineClient, createNodePoolResp.OpcWorkRequestId)
	if err != nil {
		logrus.Debugf("get work request failed with err %v", err)
		return err
	}
	// Wait for at least one individual nodes in the node pool to be created
	if state.NodePool.QuantityPerSubnet > 0 {
		nodePoolID := getResourceID(workReqRespNodePool.Resources, containerengine.WorkRequestResourceActionTypeCreated,
			string(containerengine.ListWorkRequestsResourceTypeNodepool))
		if nodePoolID == nil {
			return fmt.Errorf("could not retrieve node pool ID")
		}

		doneWaiting := false
		for retry := 15; retry > 0; retry-- {
			np, err := mgr.GetNodePoolByID(ctx, *nodePoolID)
			if err != nil {
				doneWaiting = true
			}
			for _, node := range np.Nodes {
				if node.LifecycleState != containerengine.NodeLifecycleStateCreating && node.LifecycleState !=
					containerengine.NodeLifecycleStateUpdating && node.LifecycleState !=
					containerengine.NodeLifecycleStateFailing {
					doneWaiting = true
					break
				}
				time.Sleep(1 * time.Minute)
			}
			if doneWaiting {
				break
			}
		}
	}

	return nil
}

func getImageID(ctx context.Context, c core.ComputeClient, compartment, shape, displayName string) (core.Image, error) {
	request := core.ListImagesRequest{
		CompartmentId:   common.String(compartment),
		OperatingSystem: common.String("Oracle Linux"),
		Shape:           common.String(shape),
	}
	r, err := c.ListImages(ctx, request)

	if err != nil {
		logrus.Debugf("listing image id's failed with err %v", err)
		return core.Image{}, err
	}

	index := -1
	for n, i := range r.Items {
		if strings.Compare(displayName, *i.DisplayName) == 0 {
			index = n
			break
		}
	}

	if index < 0 {
		logrus.Debugf("unable to find an image for displayName: %s", displayName)
		return core.Image{}, fmt.Errorf("unable to retrieve image %s", displayName)
	}

	return r.Items[index], err
}

// GetNodePoolByID returns the node pool with the specified Id, or an error.
func (mgr *ClusterManagerClient) GetNodePoolByID(ctx context.Context, nodePoolID string) (containerengine.NodePool, error) {

	logrus.Debugf("getting node pool with node pool ID %s", nodePoolID)

	if len(nodePoolID) == 0 {
		return containerengine.NodePool{}, fmt.Errorf("nodePoolID must be set to retrieve the node pool")
	}

	req := containerengine.GetNodePoolRequest{}
	req.NodePoolId = common.String(nodePoolID)

	resp, err := mgr.containerEngineClient.GetNodePool(ctx, req)
	if err != nil {
		logrus.Debugf("get node pool request failed with err %v", err)
		return containerengine.NodePool{}, err
	}

	return resp.NodePool, nil
}

// ScaleNodePool updates the number of nodes in the node pool, or an error.
func (mgr *ClusterManagerClient) ScaleNodePool(ctx context.Context, nodePoolID string, numNodes int, compartmentID string) error {
	logrus.Debugf("scaling node pool %s to %d nodes", nodePoolID, numNodes)

	npReq := containerengine.UpdateNodePoolRequest{}
	npReq.NodePoolId = common.String(nodePoolID)
	npReq.NodeConfigDetails = &containerengine.UpdateNodePoolNodeConfigDetails{
		Size: common.Int(numNodes),
	}

	_, err := mgr.containerEngineClient.UpdateNodePool(ctx, npReq)
	if err != nil {
		logrus.Debugf("scale node pool request failed with err %v", err)
		return err
	}

	// TODO consider optionally waiting until request is complete
	return nil
}

// UpdateKubernetesMasterVersion updates the version of Kubernetes on the master(s),
// or an error.
func (mgr *ClusterManagerClient) UpdateMasterKubernetesVersion(ctx context.Context, clusterID, version string) error {

	logrus.Debugf("updating master Kubernetes version of cluster ID %s to %s", clusterID, version)

	if len(clusterID) == 0 {
		return fmt.Errorf("clusterID must be set to upgrade the master(s)")
	}

	clReq := containerengine.UpdateClusterRequest{}
	clReq.ClusterId = common.String(clusterID)
	clReq.KubernetesVersion = common.String(version)

	cl, err := mgr.GetClusterByID(ctx, clusterID)
	if err == nil {
		logrus.Debugf("current Kubernetes version of cluster is %s", *cl.KubernetesVersion)
	} else {
		logrus.Debugf("cluster ID lookup failed with error %v", err)
		return err
	}

	cpRes, err := mgr.containerEngineClient.UpdateCluster(ctx, clReq)
	if err != nil {
		logrus.Debugf("update Kubernetes version on cluster failed with err %v", err)
		return err
	}

	// wait until node pool deletion work request complete
	logrus.Debugf("waiting for cluster master version update...")
	_, err = waitUntilWorkRequestComplete(mgr.containerEngineClient, cpRes.OpcWorkRequestId)
	if err != nil {
		logrus.Debugf("get work request failed with err %v", err)
		return err
	}

	return nil
}

// UpdateNodepoolKubernetesVersion updates the version of Kubernetes on (new)
// worker that will be added to the node pool. Be sure to call
// UpdateKubernetesMasterVersion before updating the version of node pools, or
// an error.
func (mgr *ClusterManagerClient) UpdateNodepoolKubernetesVersion(ctx context.Context, nodePoolID, version string) error {

	logrus.Debugf("updating node pool Kubernetes version of node pool ID %s to %s", nodePoolID, version)

	if len(nodePoolID) == 0 {
		return fmt.Errorf("nodePoolID must be set to upgrade the node pool")
	}

	npReq := containerengine.UpdateNodePoolRequest{}
	npReq.NodePoolId = common.String(nodePoolID)
	npReq.KubernetesVersion = common.String(version)

	np, err := mgr.GetNodePoolByID(ctx, nodePoolID)
	if err == nil {
		logrus.Debugf("current Kubernetes version of node pool is %s", *np.KubernetesVersion)
	} else {
		logrus.Debugf("node pool lookup failed with error %v", err)
		return err
	}

	// New nodes added to this node pool will run the updated version
	_, err = mgr.containerEngineClient.UpdateNodePool(ctx, npReq)
	if err != nil {
		logrus.Debugf("update Kubernetes version on node pool failed with err %v", err)
		return err
	}

	// TODO consider optionally waiting until request is complete
	return nil
}

// GetVcnIDByClusterID returns the VCN ID for the existing cluster with the
// specified Id, or an error.
func (mgr *ClusterManagerClient) GetVcnIDByClusterID(ctx context.Context, clusterID string) (string, error) {
	logrus.Debugf("getting cluster VCN with cluster ID %s", clusterID)

	cluster, err := mgr.GetClusterByID(ctx, clusterID)
	if err != nil {
		return "", err
	}

	return *cluster.VcnId, nil
}

// GetVcnIDByName returns the VCN ID of the VCN with the specified name in the
// specified compartment or an error if it is not found.
func (mgr *ClusterManagerClient) GetVcnIDByName(ctx context.Context, compartmentID, displayName string) (string, error) {
	vcn, err := mgr.GetVcnByName(ctx, compartmentID, displayName)
	if vcn.Id == nil {
		return "", err
	}
	return *vcn.Id, err
}

// GetVcnIDByName returns the VCN with the specified name in the specified compartment or an error if it is not found.
func (mgr *ClusterManagerClient) GetVcnByName(ctx context.Context, compartmentID, displayName string) (core.Vcn, error) {
	logrus.Debugf("getting VCN with name %s", displayName)

	vcn := core.Vcn{}

	if len(compartmentID) == 0 {
		return vcn, fmt.Errorf("compartmentID must be set to retrieve its VCN")
	} else if len(displayName) == 0 {
		return vcn, fmt.Errorf("displayName must be set to retrieve its VCN")
	}

	listVcnsReq := core.ListVcnsRequest{}
	listVcnsReq.CompartmentId = common.String(compartmentID)
	listVcnsReq.DisplayName = common.String(displayName)

	listVcnsResp, err := mgr.virtualNetworkClient.ListVcns(ctx, listVcnsReq)
	if err != nil {
		logrus.Debugf("list VCNs failed with err %v", err)
		return vcn, err
	}
	for _, vcn := range listVcnsResp.Items {
		if *vcn.DisplayName == displayName {
			return vcn, nil
		}
	}

	return vcn, fmt.Errorf("%s not found", displayName)
}

// GetSubnetIDByName returns the subnet ID of the subnet with the specified name
// in the specified VCN and compartment, or an error if it is not found.
func (mgr *ClusterManagerClient) GetSubnetIDByName(ctx context.Context, compartmentID, vcnID, displayName string) (string, error) {
	subnet, err := mgr.GetSubnetByName(ctx, compartmentID, vcnID, displayName)
	if subnet.Id == nil {
		return "", err
	}

	return *subnet.Id, err
}

// GetSubnetByName returns the subnet with the specified name in the specified
// VCN and compartment, or an error if it is not found.
func (mgr *ClusterManagerClient) GetSubnetByName(ctx context.Context, compartmentID, vcnID, displayName string) (core.Subnet, error) {
	logrus.Debugf("getting subnet with name %s", displayName)

	subnet := core.Subnet{}

	if len(compartmentID) == 0 {
		return subnet, fmt.Errorf("compartmentID must be set to get the subnet")
	} else if len(displayName) == 0 {
		return subnet, fmt.Errorf("displayName must be set to get the subnet")
	}

	listSubnetsReq := core.ListSubnetsRequest{}
	listSubnetsReq.CompartmentId = common.String(compartmentID)
	listSubnetsReq.VcnId = common.String(vcnID)
	listSubnetsReq.DisplayName = common.String(displayName)

	listSubnetsResp, err := mgr.virtualNetworkClient.ListSubnets(ctx, listSubnetsReq)
	if err != nil {
		logrus.Debugf("list subnets failed with err %v", err)
		return subnet, err
	}
	for _, subnet := range listSubnetsResp.Items {
		if *subnet.DisplayName == displayName {
			return subnet, nil
		}
	}

	return subnet, fmt.Errorf("%s not found", displayName)
}

// GetSubnetById returns the subnet with the specified id, or an error if it is not found.
func (mgr *ClusterManagerClient) GetSubnetById(ctx context.Context, subnetId string) (core.Subnet, error) {
	logrus.Debugf("getting subnet with ID %s", subnetId)

	subnet := core.Subnet{}

	if len(subnetId) == 0 {
		return subnet, fmt.Errorf("subnetId must be set to get the subnet")
	}

	getSubnetsReq := core.GetSubnetRequest{}
	getSubnetsReq.SubnetId = common.String(subnetId)

	getSubnetResp, err := mgr.virtualNetworkClient.GetSubnet(ctx, getSubnetsReq)
	if err != nil {
		logrus.Debugf("list subnets failed with err %v", err)
		return subnet, err
	}

	return getSubnetResp.Subnet, nil
}

// ListSubnetIdsInVcn returns the subnet IDs of any and all subnets in the
// specified VCN.
func (mgr *ClusterManagerClient) ListSubnetIdsInVcn(ctx context.Context, compartmentID, vcnID string) ([]string, error) {
	logrus.Debugf("list subnet Ids called")

	var ids []string

	if len(compartmentID) == 0 {
		return ids, fmt.Errorf("compartmentID must be set to list the subnets in the VCN")
	} else if len(vcnID) == 0 {
		return ids, fmt.Errorf("vcnID must be set to list the subnets in the VCN")
	}

	listSubnetsReq := core.ListSubnetsRequest{}
	listSubnetsReq.CompartmentId = common.String(compartmentID)
	listSubnetsReq.VcnId = common.String(vcnID)

	listSubnetsResp, err := mgr.virtualNetworkClient.ListSubnets(ctx, listSubnetsReq)
	if err != nil {
		logrus.Debugf("list subnets failed with err %v", err)
		return ids, err
	}
	for _, subnet := range listSubnetsResp.Items {
		ids = append(ids, *subnet.Id)
	}

	// subnet ids may be empty
	return ids, nil
}

// ListRouteTableIdsInVcn returns the route table IDs of any and all route tables
// in the specified VCN.
func (mgr *ClusterManagerClient) ListRouteTableIdsInVcn(ctx context.Context, compartmentID, vcnID string) ([]string, error) {
	logrus.Debugf("list route table Ids called")

	var ids []string

	if len(compartmentID) == 0 {
		return ids, fmt.Errorf("compartmentID must be set to list the route table in the VCN")
	} else if len(vcnID) == 0 {
		return ids, fmt.Errorf("vcnID must be set to list the route table in the VCN")
	}

	listRouteTablesReq := core.ListRouteTablesRequest{}
	listRouteTablesReq.CompartmentId = common.String(compartmentID)
	listRouteTablesReq.VcnId = common.String(vcnID)

	listRouteTablesResp, err := mgr.virtualNetworkClient.ListRouteTables(ctx, listRouteTablesReq)
	if err != nil {
		logrus.Debugf("list route tables failed with err %v", err)
		return ids, err
	}
	for _, rt := range listRouteTablesResp.Items {
		ids = append(ids, *rt.Id)
	}

	// route table ids may be empty
	return ids, nil
}

// ListInternetGatewayIdsInVcn returns the route table IDs of any and all
// Internet gateways in the specified VCN.
func (mgr *ClusterManagerClient) ListInternetGatewayIdsInVcn(ctx context.Context, compartmentID, vcnID string) ([]string, error) {
	logrus.Debugf("list Internet gateway Ids called")

	var ids []string

	if len(compartmentID) == 0 {
		return ids, fmt.Errorf("compartmentID must be set to list the Internet gateway in the VCN")
	} else if len(vcnID) == 0 {
		return ids, fmt.Errorf("vcnID must be set to list the Internet gateway in the VCN")
	}

	listInternetGatewaysReq := core.ListInternetGatewaysRequest{}
	listInternetGatewaysReq.CompartmentId = common.String(compartmentID)
	listInternetGatewaysReq.VcnId = common.String(vcnID)

	listRouteTablesResp, err := mgr.virtualNetworkClient.ListInternetGateways(ctx, listInternetGatewaysReq)
	if err != nil {
		logrus.Debugf("list Internet gateways failed with err %v", err)
		return ids, err
	}
	for _, ig := range listRouteTablesResp.Items {
		ids = append(ids, *ig.Id)
	}

	// route table ids may be empty
	return ids, nil
}

// ListNatGatewayIdsInVcn returns the NAT gateway IDs of any and all NAT
// gateways in the specified VCN.
func (mgr *ClusterManagerClient) ListNatGatewayIdsInVcn(ctx context.Context, compartmentID, vcnID string) ([]string, error) {
	logrus.Debugf("list NAT gateway Ids called")

	var ids []string

	if len(compartmentID) == 0 {
		return ids, fmt.Errorf("compartmentID must be set to list the NAT gateway in the VCN")
	} else if len(vcnID) == 0 {
		return ids, fmt.Errorf("vcnID must be set to list the NAT gateway in the VCN")
	}

	listInternetGatewaysReq := core.ListNatGatewaysRequest{}
	listInternetGatewaysReq.CompartmentId = common.String(compartmentID)
	listInternetGatewaysReq.VcnId = common.String(vcnID)

	listNATGatewaysResp, err := mgr.virtualNetworkClient.ListNatGateways(ctx, listInternetGatewaysReq)
	if err != nil {
		logrus.Debugf("list NAT gateways failed with err %v", err)
		return ids, err
	}
	for _, nGW := range listNATGatewaysResp.Items {
		ids = append(ids, *nGW.Id)
	}

	// NAT gateway ids may be empty
	return ids, nil
}

// ListSecurityListIdsInVcn returns the security list IDs of any and all security
// lists in the specified VCN.
func (mgr *ClusterManagerClient) ListSecurityListIdsInVcn(ctx context.Context, compartmentID, vcnID string) ([]string, error) {
	logrus.Debugf("list security list Ids called")

	var ids []string

	if len(compartmentID) == 0 {
		return ids, fmt.Errorf("compartmentID must be set to list the security lists in the VCN")
	} else if len(vcnID) == 0 {
		return ids, fmt.Errorf("vcnID must be set to list the security lists in the VCN")
	}

	listSecurityListsReq := core.ListSecurityListsRequest{}
	listSecurityListsReq.CompartmentId = common.String(compartmentID)
	listSecurityListsReq.VcnId = common.String(vcnID)

	listSecurityListsResp, err := mgr.virtualNetworkClient.ListSecurityLists(ctx, listSecurityListsReq)
	if err != nil {
		logrus.Debugf("list security lists failed with err %v", err)
		return ids, err
	}
	for _, sl := range listSecurityListsResp.Items {
		ids = append(ids, *sl.Id)
	}

	// route table ids may be empty
	return ids, nil
}

// ListNodepoolIdsInCluster returns the node pool IDs of any and all node pools
// in the specified cluster.
func (mgr *ClusterManagerClient) ListNodepoolIdsInCluster(ctx context.Context, compartmentID, clusterID string) ([]string, error) {
	logrus.Debugf("list node pool ID(s) for cluster ID %s", clusterID)

	var ids []string

	if len(compartmentID) == 0 {
		return ids, fmt.Errorf("compartmentID must be set to list the node pools in the cluster")
	} else if len(clusterID) == 0 {
		return ids, fmt.Errorf("clusterID must be set to list the node pools in the cluster")
	}

	req := containerengine.ListNodePoolsRequest{}
	req.CompartmentId = common.String(compartmentID)
	req.ClusterId = common.String(clusterID)

	resp, err := mgr.containerEngineClient.ListNodePools(ctx, req)

	if err != nil {
		logrus.Debugf("list Node Pools request failed with err %v", err)
		return ids, err
	}

	for _, np := range resp.Items {
		ids = append(ids, *np.Id)
	}

	// subnet ids may be empty
	return ids, nil
}

// DeleteNodePool deletes the node pool with the specified ID, or an error
func (mgr *ClusterManagerClient) DeleteNodePool(ctx context.Context, nodePoolID string) error {
	logrus.Debugf("delete node pool with ID %s", nodePoolID)

	if len(nodePoolID) == 0 {
		return fmt.Errorf("nodePoolID must be set to delete the node pool")
	}

	req := containerengine.DeleteNodePoolRequest{}
	req.NodePoolId = common.String(nodePoolID)

	deleteNodePoolResp, err := mgr.containerEngineClient.DeleteNodePool(ctx, req)
	if err != nil {
		logrus.Debugf("delete node pool request failed with err %v", err)
		return err
	}

	// wait until node pool deletion work request complete
	logrus.Debugf("waiting for node pool to be deleted...")
	// TODO better to poll instead of sleep
	time.Sleep(mgr.sleepDuration * time.Second)
	_, err = waitUntilWorkRequestComplete(mgr.containerEngineClient, deleteNodePoolResp.OpcWorkRequestId)
	if err != nil {
		logrus.Debugf("get work request failed with err %v", err)
		return err
	}

	return nil
}

// DeleteCluster deletes the cluster with the specified ID, or an error.
func (mgr *ClusterManagerClient) DeleteCluster(ctx context.Context, clusterID string) error {
	logrus.Debugf("deleting cluster with cluster ID %s", clusterID)

	if len(clusterID) == 0 {
		return fmt.Errorf("clusterID must be set to delete the cluster")
	}

	req := containerengine.DeleteClusterRequest{}
	req.ClusterId = common.String(clusterID)

	deleteClusterResp, err := mgr.containerEngineClient.DeleteCluster(ctx, req)
	if err != nil {
		logrus.Debugf("delete cluster request failed with err %v", err)
		return err
	}

	// wait until cluster deletion work request complete
	logrus.Debugf("waiting for cluster to be deleted...")
	// initial delay since subsequent back-off function waits longer each time the retry fails
	time.Sleep(time.Minute * 3)
	_, err = waitUntilWorkRequestComplete(mgr.containerEngineClient, deleteClusterResp.OpcWorkRequestId)
	if err != nil {
		logrus.Debugf("get work request failed with err %v", err)
		return err
	}

	return nil
}

// DeleteVCN deletes the VCN and its associated resources (subnets, attached
// gateways, etc.) with the specified ID, or an error.
func (mgr *ClusterManagerClient) DeleteVCN(ctx context.Context, vcnID string) error {

	logrus.Debugf("deleting VCN with VCN ID %s", vcnID)

	if len(vcnID) == 0 {
		return fmt.Errorf("vcnID must be set to delete the VCN")
	}

	getVCNReq := core.GetVcnRequest{}
	getVCNReq.VcnId = common.String(vcnID)

	getVCNResp, err := mgr.virtualNetworkClient.GetVcn(ctx, getVCNReq)
	if err != nil {
		logrus.Debugf("get VCN failed with err %v", err)
		return err
	}

	// The VCN must be completely empty (meaning no subnets, attached gateways,
	// or security lists)

	// Delete Route Rules
	listRouteTblsReq := core.ListRouteTablesRequest{}
	listRouteTblsReq.VcnId = common.String(vcnID)
	listRouteTblsReq.CompartmentId = getVCNResp.Vcn.CompartmentId
	rtResp, err := mgr.virtualNetworkClient.ListRouteTables(ctx, listRouteTblsReq)
	if err != nil {
		logrus.Debugf("list route tables failed with err %v", err)
		return err
	}
	for _, rt := range rtResp.Items {

		updateRTReq := core.UpdateRouteTableRequest{}
		updateRTReq.RtId = rt.Id
		updateRTReq.RouteRules = []core.RouteRule{}

		logrus.Debugf("removing default route rule from route table %s", *rt.Id)
		_, err = mgr.virtualNetworkClient.UpdateRouteTable(ctx, updateRTReq)
		if err != nil {
			logrus.Debugf("update route table failed with err %v", err)
			return err
		}
	}

	// Delete Internet Gateways from VCN
	listIGsReq := core.ListInternetGatewaysRequest{}
	listIGsReq.VcnId = common.String(vcnID)
	listIGsReq.CompartmentId = getVCNResp.Vcn.CompartmentId
	igsResp, err := mgr.virtualNetworkClient.ListInternetGateways(ctx, listIGsReq)
	if err != nil {
		logrus.Debugf("list internet gateway(s) failed with err %v", err)
		return err
	}
	for _, ig := range igsResp.Items {
		deleteIGReq := core.DeleteInternetGatewayRequest{}
		deleteIGReq.IgId = ig.Id
		logrus.Debugf("deleting internet gateway %s", *ig.Id)
		_, err = mgr.virtualNetworkClient.DeleteInternetGateway(ctx, deleteIGReq)
		if err != nil {
			logrus.Debugf("warning: delete internet gateway failed with err %v", err)
			// Continue tearing down.
		}
	}

	// Delete all the subnets from VCN
	listSubnetsReq := core.ListSubnetsRequest{}
	listSubnetsReq.VcnId = common.String(vcnID)
	listSubnetsReq.CompartmentId = getVCNResp.Vcn.CompartmentId
	listSubnetResp, err := mgr.virtualNetworkClient.ListSubnets(ctx, listSubnetsReq)
	if err != nil {
		logrus.Debugf("list subnets failed with err %v", err)
		return err
	}
	for _, subnet := range listSubnetResp.Items {
		deleteSubnetReq := core.DeleteSubnetRequest{}
		deleteSubnetReq.SubnetId = subnet.Id
		logrus.Debugf("deleting subnet %s", *subnet.Id)
		_, err := mgr.virtualNetworkClient.DeleteSubnet(ctx, deleteSubnetReq)
		if err != nil {
			logrus.Debugf("warning: delete subnet failed with err %v", err)
			// Continue tearing down.
		}
	}
	// TODO better to poll instead of sleep
	time.Sleep(mgr.sleepDuration * time.Second)

	// Delete all security lists from VCN
	listSecurityListsReq := core.ListSecurityListsRequest{}
	listSecurityListsReq.VcnId = common.String(vcnID)
	listSecurityListsReq.CompartmentId = getVCNResp.Vcn.CompartmentId
	listSecurityListsResp, err := mgr.virtualNetworkClient.ListSecurityLists(ctx, listSecurityListsReq)
	if err != nil {
		logrus.Debugf("list security lists failed with err %v", err)
		return err
	}
	for _, securityList := range listSecurityListsResp.Items {
		deleteSecurityListReq := core.DeleteSecurityListRequest{}
		deleteSecurityListReq.SecurityListId = securityList.Id
		logrus.Debugf("deleting security list (%s)", *securityList.Id)
		_, err := mgr.virtualNetworkClient.DeleteSecurityList(ctx, deleteSecurityListReq)
		if err != nil {
			logrus.Debugf("warning: delete security list failed with err %v", err)
			// Continue tearing down.
		}
	}

	// Delete the route tables now that the subnets are gone...
	for _, rt := range rtResp.Items {
		deleteRTReq := core.DeleteRouteTableRequest{}
		deleteRTReq.RtId = rt.Id
		logrus.Debugf("removing route table %s", *rt.Id)
		_, err = mgr.virtualNetworkClient.DeleteRouteTable(ctx, deleteRTReq)
		if err != nil {
			logrus.Debugf("warning: delete route table failed with err %v", err)
			// Continue tearing down.
		}
	}

	// Delete NAT Gateway(s) from VCN
	listNGsReq := core.ListNatGatewaysRequest{}
	listNGsReq.VcnId = common.String(vcnID)
	listNGsReq.CompartmentId = getVCNResp.Vcn.CompartmentId
	ngsResp, err := mgr.virtualNetworkClient.ListNatGateways(ctx, listNGsReq)
	if err != nil {
		logrus.Debugf("list NAT gateway(s) failed with err %v", err)
		return err
	}
	for _, ng := range ngsResp.Items {
		deleteNGReq := core.DeleteNatGatewayRequest{}
		deleteNGReq.NatGatewayId = ng.Id
		logrus.Debugf("deleting NAT gateway %s", *ng.Id)
		_, err = mgr.virtualNetworkClient.DeleteNatGateway(ctx, deleteNGReq)
		if err != nil {
			logrus.Debugf("warning: delete NAT gateway failed with err %v", err)
			// Continue tearing down.
		}
	}

	// Delete Service Gateway(s) from VCN
	listSGsReq := core.ListServiceGatewaysRequest{}
	listSGsReq.VcnId = common.String(vcnID)
	listSGsReq.CompartmentId = getVCNResp.Vcn.CompartmentId
	sgResp, err := mgr.virtualNetworkClient.ListServiceGateways(ctx, listSGsReq)
	if err != nil {
		logrus.Debugf("list Service gateway(s) failed with err %v", err)
		return err
	}
	for _, sg := range sgResp.Items {
		deleteSGReq := core.DeleteServiceGatewayRequest{}
		deleteSGReq.ServiceGatewayId = sg.Id
		logrus.Debugf("deleting Service gateway %s", *sg.Id)
		_, err = mgr.virtualNetworkClient.DeleteServiceGateway(ctx, deleteSGReq)
		if err != nil {
			logrus.Debugf("warning: delete Service gateway failed with err %v", err)
			// Continue tearing down.
		}
	}

	// Finally, delete the VCN itself
	vcnRequest := core.DeleteVcnRequest{}
	vcnRequest.VcnId = common.String(vcnID)

	logrus.Debugf("deleting VCN (%s)", vcnID)
	_, err = mgr.virtualNetworkClient.DeleteVcn(ctx, vcnRequest)
	if err != nil {
		logrus.Debugf("delete virtual-network request failed with err %v", err)
		return err
	}

	return nil
}

// GetKubeconfigByClusterID is a wrapper for the CreateKubeconfig operation that
// that handles errors and unmarshaling, or an error.
func (mgr *ClusterManagerClient) GetKubeconfigByClusterID(ctx context.Context, clusterID, region string) (store.KubeConfig, string, error) {
	logrus.Debugf("getting KUBECONFIG with cluster ID %s", clusterID)

	kubeconfig := &store.KubeConfig{}

	if len(clusterID) == 0 {
		return store.KubeConfig{}, "", fmt.Errorf("clusterID must be set to get the KUBECONFIG file")
	}

	response, err := mgr.containerEngineClient.CreateKubeconfig(ctx, containerengine.CreateKubeconfigRequest{
		ClusterId: &clusterID,
	})
	if err != nil {
		logrus.Debugf("error creating kubeconfig %v", err)
		return store.KubeConfig{}, "", err
	}

	content, err := ioutil.ReadAll(response.Content)
	if err != nil {
		logrus.Debugf("error reading kubeconfig response content %v", err)
		return store.KubeConfig{}, "", err
	}

	err = yaml.Unmarshal(content, kubeconfig)
	if err != nil {
		logrus.Debugf("error unmarshalling kubeconfig %v", err)
		return store.KubeConfig{}, "", nil
	}

	if len(kubeconfig.Users) > 0 && kubeconfig.Users[0].User.Token == "" {
		// Generate the token here rather than using exec credentials, which is
		// not supported by rancher's KubeConfig and ClusterInfo types.
		logrus.Info("generating a /v2 (exec based) kubeconfig token")
		requestSigner := common.RequestSigner(mgr.configuration, common.DefaultGenericHeaders(), common.DefaultBodyHeaders())
		interceptor := func(r *http.Request) error {
			return nil
		}
		expiringToken, err := generateToken(newTokenSigner(requestSigner, interceptor), region, clusterID)
		if err != nil {
			logrus.Debugf("error generating /v2 kubeconfig token %v", err)
			return store.KubeConfig{}, "", nil
		}
		kubeconfig.Users[0].User.Token = expiringToken
	}

	return *kubeconfig, string(content), nil
}

// CreateNodeSubnets creates (public or private) regional node subnet, or an error.
// TODO stop passing in state
func (mgr *ClusterManagerClient) CreateNodeSubnets(ctx context.Context, state *State, vcnID, subnetRouteID string, isPrivate bool, securityListIds []string) ([]string, error) {

	if isPrivate {
		logrus.Debugf("creating public regional node subnet in VCN ID %s", vcnID)
	} else {
		logrus.Debugf("creating private regional node subnet in VCN ID %s", vcnID)
	}

	var subnetIds = []string{}
	if state == nil {
		return subnetIds, fmt.Errorf("valid state is required")
	}

	// create a subnet in different availability domain
	req := identity.ListAvailabilityDomainsRequest{}
	req.CompartmentId = &state.CompartmentID

	// Create regional subnet
	subnet1, err := mgr.CreateSubnetWithDetails(
		common.String(state.Network.NodePoolSubnetName),
		common.String(nodeCIDRBlock),
		common.String(state.Network.NodePoolSubnetDnsDomainName),
		nil,
		common.String(vcnID), common.String(subnetRouteID), isPrivate, securityListIds, state)
	if err != nil {
		logrus.Debugf("create new node subnet failed with err %v", err)
		return subnetIds, err
	}
	subnetIds = append(subnetIds, *subnet1.Id)

	return subnetIds, nil
}

// CreateControlPlaneSubnet creates (public or private) regional subnet for the k8s control-plane, or an error.
func (mgr *ClusterManagerClient) CreateControlPlaneSubnet(ctx context.Context, state *State, vcnID, subnetRouteID string, isPrivate bool, securityListIds []string) (string, error) {

	if isPrivate {
		logrus.Debugf("creating private regional control-plane subnet in VCN ID %s", vcnID)
	} else {
		logrus.Debugf("creating public regional control-plane subnet in VCN ID %s", vcnID)
	}

	var subnetId = ""
	if state == nil {
		return "", fmt.Errorf("valid state is required")
	}

	// create a subnet in different availability domain
	req := identity.ListAvailabilityDomainsRequest{}
	req.CompartmentId = &state.CompartmentID

	var routeId *string = nil
	if len(subnetRouteID) > 0 {
		routeId = common.String(subnetRouteID)
	}

	// Create regional subnet
	subnet1, err := mgr.CreateSubnetWithDetails(
		common.String(controlPlaneSubnetName),
		common.String(controlPlaneCIDRBlock),
		common.String(controlPlaneSubnetName),
		nil,
		common.String(vcnID), routeId, isPrivate, securityListIds, state)
	if err != nil {
		logrus.Debugf("create new control-plane subnet failed with err %v", err)
		return subnetId, err
	}
	subnetId = *subnet1.Id

	return subnetId, nil
}

// CreateServiceSubnets creates the regional (public) service subnet (i.e. load balancer
// subnet), or an error.
func (mgr *ClusterManagerClient) CreateServiceSubnets(ctx context.Context, state *State, vcnID, subnetRouteID string, isPrivate bool, securityListIds []string) ([]string, error) {
	logrus.Debugf("creating service / LB subnet(s) in VCN ID %s", vcnID)

	var subnetIds = []string{}
	if state == nil {
		return subnetIds, fmt.Errorf("valid state is required")
	}

	// Create regional subnet for services
	var svcSubnetName = ""
	if state.Network.ServiceLBSubnet1Name == "" {
		svcSubnetName = state.Network.ServiceSubnetDnsDomainName
	} else {
		svcSubnetName = state.Network.ServiceLBSubnet1Name
	}
	// Create regional subnet
	subnet, err := mgr.CreateSubnetWithDetails(common.String(svcSubnetName),
		common.String(serviceCIDRBlock),
		common.String(state.Network.ServiceSubnetDnsDomainName),
		nil,
		common.String(vcnID), nil, isPrivate, securityListIds, state)
	if err != nil {
		logrus.Debugf("create new service subnet failed with err %v", err)
		return subnetIds, err
	}
	subnetIds = append(subnetIds, *subnet.Id)

	return subnetIds, nil
}

// CreateBastionSubnets creates the (public) bastion subnet(s), or an error.
func (mgr *ClusterManagerClient) CreateBastionSubnets(ctx context.Context, state *State, vcnID, subnetRouteID string, isPrivate bool, securityListIds []string) ([]string, error) {
	logrus.Debugf("creating bastion subnet(s) in VCN ID %s", vcnID)

	var subnetIds = []string{}
	if state == nil {
		return subnetIds, fmt.Errorf("valid state is required")
	}

	// create a subnet in different availability domain
	req := identity.ListAvailabilityDomainsRequest{}
	req.CompartmentId = &state.CompartmentID
	ads, err := mgr.identityClient.ListAvailabilityDomains(ctx, req)
	if err != nil {
		return subnetIds, err
	}

	if len(ads.Items) < 1 {
		return subnetIds, fmt.Errorf("at least 1 availability domains are required to host the bastion subnet")
	}

	// Create regional subnet
	var subnetName = bastionSubnetName
	subnet, err := mgr.CreateSubnetWithDetails(common.String(subnetName),
		common.String(bastionCIDRBlock),
		common.String(bastionSubnetName),
		nil,
		common.String(vcnID), nil, isPrivate, securityListIds, state)
	if err != nil {
		logrus.Debugf("create new bastion subnet failed with err %v", err)
		return subnetIds, err
	}
	subnetIds = append(subnetIds, *subnet.Id)

	return subnetIds, nil
}

// CreateSubnetWithDetails creates a new subnet in the specified VCN, or an error.
// TODO stop passing in state
func (mgr *ClusterManagerClient) CreateSubnetWithDetails(displayName *string, cidrBlock *string, dnsLabel *string, availableDomain *string, vcnID *string, routeTableID *string, isPrivate bool, securityListIds []string, state *State) (core.Subnet, error) {

	if state == nil {
		return core.Subnet{}, fmt.Errorf("valid state is required")
	}

	ctx := context.Background()

	// create a new subnet
	request := core.CreateSubnetRequest{}
	request.AvailabilityDomain = availableDomain
	request.CompartmentId = &state.CompartmentID
	request.CidrBlock = cidrBlock
	request.DisplayName = displayName
	request.DnsLabel = dnsLabel
	request.VcnId = vcnID
	request.SecurityListIds = securityListIds
	request.ProhibitPublicIpOnVnic = &isPrivate
	request.RouteTableId = routeTableID

	request.RequestMetadata = helpers.GetRequestMetadataWithDefaultRetryPolicy()

	response, err := mgr.virtualNetworkClient.CreateSubnet(ctx, request)
	if err != nil {
		logrus.Debugf("create subnet request failed with err %v", err)
		return core.Subnet{}, err
	}

	// retry condition check, stop until return true
	pollUntilAvailable := func(r common.OCIOperationResponse) bool {
		if converted, ok := r.Response.(core.GetSubnetResponse); ok {
			return converted.LifecycleState != core.SubnetLifecycleStateAvailable
		}
		return true
	}

	pollGetRequest := core.GetSubnetRequest{
		SubnetId:        response.Id,
		RequestMetadata: helpers.GetRequestMetadataWithCustomizedRetryPolicy(pollUntilAvailable),
	}

	// wait for lifecycle become running
	_, pollErr := mgr.virtualNetworkClient.GetSubnet(ctx, pollGetRequest)
	helpers.FatalIfError(pollErr)

	return response.Subnet, nil
}

// CreateVCNAndNetworkResources creates a new Virtual Cloud Network and
// required resources including security lists, Internet Gateway, default route
// rule, etc., or an error.
func (mgr *ClusterManagerClient) CreateVCNAndNetworkResources(state *State) (string, string, []string, []string, error) {

	logrus.Debugf("create virtual cloud network called.")
	if state == nil {
		return "", "", nil, nil, fmt.Errorf("valid state is required")
	}

	ctx := context.Background()

	// create a new VCNID and sub-resources
	vcnRequest := core.CreateVcnRequest{}
	vcnRequest.CidrBlock = common.String(vcnCIDRBlock)
	vcnRequest.CompartmentId = &state.CompartmentID
	vcnRequest.DisplayName = common.String(state.Network.VCNName)
	dnsLabel := generateUniqueLabel("kontainer", 13)
	vcnRequest.DnsLabel = common.String(dnsLabel)

	r, err := mgr.virtualNetworkClient.CreateVcn(ctx, vcnRequest)
	if err != nil {
		logrus.Debugf("create virtual-network request failed with err %v", err)
		return "", "", nil, nil, err
	}
	// TODO better to poll instead of sleep
	time.Sleep(mgr.sleepDuration * time.Second)

	var trueVar = true
	// Create an internet gateway
	internetGatewayReq := core.CreateInternetGatewayRequest{
		CreateInternetGatewayDetails: core.CreateInternetGatewayDetails{
			CompartmentId: &state.CompartmentID,
			VcnId:         r.Vcn.Id,
			IsEnabled:     &trueVar,
			DisplayName:   common.String("oke-igw"),
		}}

	igResp, err := mgr.virtualNetworkClient.CreateInternetGateway(ctx, internetGatewayReq)
	helpers.FatalIfError(err)

	routeTablesReq := core.ListRouteTablesRequest{}
	routeTablesReq.VcnId = r.Vcn.Id
	routeTablesReq.CompartmentId = common.String(state.CompartmentID)
	routeTablesResp, err := mgr.virtualNetworkClient.ListRouteTables(ctx, routeTablesReq)
	if err != nil {
		logrus.Debugf("list route tables request failed with err %v", err)
		return "", "", nil, nil, err
	}
	if len(routeTablesResp.Items) != 1 {
		return "", "", nil, nil, fmt.Errorf("cannot find default route rule for the VCN")
	}

	// Default route by default
	nodeSubnetRouteID := routeTablesResp.Items[0].Id
	controlPlaneRouteID := routeTablesResp.Items[0].Id

	// Add a route rule in the route table that directs internet-bound traffic
	// to the internet gateway created above.
	updateRouteTableReq := core.UpdateRouteTableRequest{}
	updateRouteTableReq.RtId = routeTablesResp.Items[0].Id
	updateRouteTableReq.DisplayName = routeTablesResp.Items[0].DisplayName
	updateRouteTableReq.RouteRules = append(updateRouteTableReq.RouteRules, core.RouteRule{Destination: common.String("0.0.0.0/0"), NetworkEntityId: igResp.InternetGateway.Id})

	_, err = mgr.virtualNetworkClient.UpdateRouteTable(ctx, updateRouteTableReq)
	if err != nil {
		logrus.Debugf("update route table request failed with err %v", err)
		return "", "", nil, nil, err
	}

	if state.PrivateControlPlane || state.PrivateNodes {

		// Create a NAT and service gateways, as well as route rules to direct desired traffic from corresponding private subnets
		natGatewayReq := core.CreateNatGatewayRequest{
			CreateNatGatewayDetails: core.CreateNatGatewayDetails{
				CompartmentId: &state.CompartmentID,
				VcnId:         r.Vcn.Id,
				DisplayName:   common.String("oke-ngw"),
				BlockTraffic:  nil,
			},
		}
		ngResp, err := mgr.virtualNetworkClient.CreateNatGateway(ctx, natGatewayReq)
		helpers.FatalIfError(err)

		// Create a Service gateway
		var allServices *core.Service
		listResponse, err := mgr.virtualNetworkClient.ListServices(ctx, core.ListServicesRequest{})
		for _, service := range listResponse.Items {
			if strings.HasSuffix(*service.Name, "Services In Oracle Services Network") {
				allServices = &service
				break
			}
		}
		if allServices == nil {
			logrus.Debug("failed to retrieve Oracle Services Network")
			return "", "", nil, nil, err
		}
		serviceGatewayReq := core.CreateServiceGatewayRequest{
			CreateServiceGatewayDetails: core.CreateServiceGatewayDetails{
				CompartmentId: &state.CompartmentID,
				VcnId:         r.Vcn.Id,
				Services:      []core.ServiceIdRequestDetails{core.ServiceIdRequestDetails{ServiceId: allServices.Id}},
				DisplayName:   common.String("oke-svcgw"),
			}}

		svcGatewayResp, err := mgr.virtualNetworkClient.CreateServiceGateway(ctx, serviceGatewayReq)
		helpers.FatalIfError(err)

		natRules := []core.RouteRule{
			{
				Destination:     common.String("0.0.0.0/0"),
				NetworkEntityId: ngResp.NatGateway.Id,
			},
			{
				Destination:     allServices.CidrBlock,
				DestinationType: core.RouteRuleDestinationTypeServiceCidrBlock,
				NetworkEntityId: svcGatewayResp.Id,
			},
		}

		createRouteTableReq := core.CreateRouteTableRequest{}
		// Add a route rule in the route table that directs service traffic to the service gateway and internet-bound
		// traffic to the NAT gateway.
		createRouteTableReq.DisplayName = common.String("oke-private-routetable")
		createRouteTableReq.RouteRules = append(natRules)
		createRouteTableReq.CompartmentId = &state.CompartmentID
		createRouteTableReq.VcnId = r.Vcn.Id

		createPrivateRouteTablesResp, err := mgr.virtualNetworkClient.CreateRouteTable(ctx, createRouteTableReq)
		helpers.FatalIfError(err)

		if state.PrivateNodes {
			// Private route for worker subnet
			nodeSubnetRouteID = createPrivateRouteTablesResp.Id
		}

		if state.PrivateControlPlane {
			// Private route for control plane subnet
			controlPlaneRouteID = createPrivateRouteTablesResp.Id

			// Create a bastion subnet to access k8s API
			bastionSecList := core.CreateSecurityListRequest{
				CreateSecurityListDetails: core.CreateSecurityListDetails{
					CompartmentId:        &state.CompartmentID,
					DisplayName:          common.String("Bastion Security List"),
					EgressSecurityRules:  []core.EgressSecurityRule{},
					IngressSecurityRules: []core.IngressSecurityRule{},
					VcnId:                r.Vcn.Id}}

			// Allow internal traffic from VCN to the bastion by default
			bastionSecList.IngressSecurityRules = append(bastionSecList.IngressSecurityRules, core.IngressSecurityRule{
				Protocol: common.String("all"),
				Source:   common.String(vcnCIDRBlock),
			})
			// Allow internal traffic from NAT gateway to the bastion by default
			bastionSecList.IngressSecurityRules = append(bastionSecList.IngressSecurityRules, core.IngressSecurityRule{
				Protocol: common.String("all"),
				Source:   ngResp.NatIp,
			})

			// Allow outgoing traffic to VCN from the bastion by default
			bastionSecList.EgressSecurityRules = append(bastionSecList.EgressSecurityRules, core.EgressSecurityRule{
				Protocol:    common.String("all"),
				Destination: common.String(vcnCIDRBlock),
			})

			bastionSecListResp, err := mgr.virtualNetworkClient.CreateSecurityList(ctx, bastionSecList)
			helpers.FatalIfError(err)

			_, err = mgr.CreateBastionSubnets(ctx, state, *r.Vcn.Id, "", false, []string{*bastionSecListResp.SecurityList.Id})
			helpers.FatalIfError(err)

			// User must configure bastion Ingress and any additional egress besides VCN CIDR
			logrus.Info("Bastion subnet created. A bastion host and security rules must be configured manually.")
		}
	}

	// Create the control-plane security list
	controlPlaneSecurityListIds, err := mgr.CreateControlPlaneSecurityList(ctx, state, r.Vcn.Id, nodeCIDRBlock, serviceCIDRBlock, controlPlaneSubnetSecurityListName)

	controlPlaneSubnetId, err := mgr.CreateControlPlaneSubnet(ctx, state, *r.Vcn.Id, *controlPlaneRouteID, state.PrivateControlPlane, controlPlaneSecurityListIds)
	helpers.FatalIfError(err)

	// Create the node security list
	nodeSecurityListIds, err := mgr.CreateNodeSecurityList(ctx, state, r.Vcn.Id, nodeCIDRBlock, serviceCIDRBlock, state.Network.NodePoolSubnetSecurityListName)

	nodeSubnet, err := mgr.CreateNodeSubnets(ctx, state, *r.Vcn.Id, *nodeSubnetRouteID, state.PrivateNodes, nodeSecurityListIds)
	helpers.FatalIfError(err)

	serviceSecurityListIds, err := mgr.CreateServiceSecurityList(ctx, state, r.Vcn.Id, state.Network.ServiceSubnetSecurityListName)

	serviceSubnet, err := mgr.CreateServiceSubnets(ctx, state, *r.Vcn.Id, "", false, serviceSecurityListIds)
	helpers.FatalIfError(err)

	return *r.Vcn.Id, controlPlaneSubnetId, serviceSubnet, nodeSubnet, nil
}

// Create the node security list
func (mgr *ClusterManagerClient) CreateNodeSecurityList(ctx context.Context, state *State, vcnId *string, nodeCidrBlock string, serviceCidrBlock string, name string) ([]string, error) {

	// Allow OKE incoming access worker nodes on port 22 for setup and maintenance
	okeCidrBlocks := []string{"130.35.0.0/16", "134.70.0.0/17", "138.1.0.0/16", "140.91.0.0/17", "147.154.0.0/16", "192.29.0.0/16"}
	okeAdminPortRange := core.PortRange{
		Max: common.Int(22),
		Min: common.Int(22),
	}

	nodeSecList := core.CreateSecurityListRequest{
		CreateSecurityListDetails: core.CreateSecurityListDetails{
			CompartmentId:        &state.CompartmentID,
			DisplayName:          common.String(name),
			EgressSecurityRules:  []core.EgressSecurityRule{},
			IngressSecurityRules: []core.IngressSecurityRule{},
			VcnId:                vcnId}}

	// Default egress rule to allow outbound traffic to the internet
	nodeSecList.EgressSecurityRules = append(nodeSecList.EgressSecurityRules, core.EgressSecurityRule{
		Protocol:    common.String("6"), // TCP
		Destination: common.String("0.0.0.0/0"),
	})
	// Allow internal traffic from other worker nodes by default
	nodeSecList.EgressSecurityRules = append(nodeSecList.EgressSecurityRules, core.EgressSecurityRule{
		Protocol:    common.String("all"),
		Destination: common.String(nodeCidrBlock),
	})

	for _, okeCidr := range okeCidrBlocks {
		nodeSecList.IngressSecurityRules = append(nodeSecList.IngressSecurityRules, core.IngressSecurityRule{
			Protocol: common.String("6"), // TCP
			Source:   common.String(okeCidr),
			TcpOptions: &core.TcpOptions{
				DestinationPortRange: &okeAdminPortRange,
			},
		})
	}
	// Allow internal traffic from other worker nodes by default
	nodeSecList.IngressSecurityRules = append(nodeSecList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol: common.String("all"),
		Source:   common.String(nodeCidrBlock),
	})
	// Allow incoming traffic on standard node ports
	nodePortRange := core.PortRange{
		Max: common.Int(32767),
		Min: common.Int(30000),
	}
	nodeSecList.IngressSecurityRules = append(nodeSecList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol: common.String("6"), // TCP
		Source:   common.String(serviceCidrBlock),
		TcpOptions: &core.TcpOptions{
			DestinationPortRange: &nodePortRange,
		},
	})
	if state.WorkerNodeIngressCidr != "" {
		nodeSecList.IngressSecurityRules = append(nodeSecList.IngressSecurityRules, core.IngressSecurityRule{
			Protocol: common.String("6"), // TCP
			Source:   common.String(state.WorkerNodeIngressCidr),
			TcpOptions: &core.TcpOptions{
				DestinationPortRange: &nodePortRange,
			},
		})
	}

	// Allow ICMP traffic from worker nodes for path discovery
	nodeSecList.IngressSecurityRules = append(nodeSecList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol:    common.String("1"),
		Description: common.String("Path discovery"),
		Source:      common.String("0.0.0.0/0"),
		IcmpOptions: &core.IcmpOptions{
			Code: common.Int(4),
			Type: common.Int(3),
		},
		IsStateless: common.Bool(false),
	})
	// TODO we could also create a service gateway and corresponding route rule.

	nodeSecListResp, err := mgr.virtualNetworkClient.CreateSecurityList(ctx, nodeSecList)
	helpers.FatalIfError(err)

	return []string{*nodeSecListResp.SecurityList.Id}, nil
}

// Create the control-plane security list
func (mgr *ClusterManagerClient) CreateControlPlaneSecurityList(ctx context.Context, state *State, vcnId *string, nodeCidrBlock string, serviceCidrBlock string, name string) ([]string, error) {

	ctrlSecurityList := core.CreateSecurityListRequest{
		CreateSecurityListDetails: core.CreateSecurityListDetails{
			CompartmentId:        &state.CompartmentID,
			DisplayName:          common.String(name),
			EgressSecurityRules:  []core.EgressSecurityRule{},
			IngressSecurityRules: []core.IngressSecurityRule{},
			VcnId:                vcnId}}

	ctrlSecurityList.IngressSecurityRules = append(ctrlSecurityList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol:    common.String("6"), // TCP
		Source:      common.String(nodeCidrBlock),
		Description: common.String("Kubernetes worker to Kubernetes API endpoint communication"),
		TcpOptions: &core.TcpOptions{
			DestinationPortRange: &core.PortRange{
				Max: common.Int(6443),
				Min: common.Int(6443),
			},
		},
	})
	// Allow ICMP traffic from worker nodes for path discovery
	ctrlSecurityList.IngressSecurityRules = append(ctrlSecurityList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol:    common.String("1"),
		Description: common.String("Path discovery"),
		Source:      common.String(nodeCidrBlock),
		IcmpOptions: &core.IcmpOptions{
			Code: common.Int(4),
			Type: common.Int(3),
		},
		IsStateless: common.Bool(false),
	})
	if !state.PrivateControlPlane {
		// Open up access to Kubernetes API endpoint if not private
		ctrlSecurityList.IngressSecurityRules = append(ctrlSecurityList.IngressSecurityRules, core.IngressSecurityRule{
			Protocol:    common.String("6"), // TCP
			Source:      common.String("0.0.0.0/0"),
			Description: common.String("External access to Kubernetes API endpoint"),
			TcpOptions: &core.TcpOptions{
				DestinationPortRange: &core.PortRange{
					Max: common.Int(6443),
					Min: common.Int(6443),
				},
			},
			IsStateless: common.Bool(false),
		})
	}
	if state.PrivateNodes {
		// Allow selected access to and from the bastion subnet by default
		ctrlSecurityList.IngressSecurityRules = append(ctrlSecurityList.IngressSecurityRules, core.IngressSecurityRule{
			Protocol:    common.String("6"), // TCP
			Source:      common.String(bastionCIDRBlock),
			Description: common.String("Bastion to Kubernetes API endpoint communication"),
			TcpOptions: &core.TcpOptions{
				DestinationPortRange: &core.PortRange{
					Max: common.Int(6443),
					Min: common.Int(6443),
				},
			},
			IsStateless: common.Bool(false),
		})
		// Default egress rule to allow outbound traffic to bastion
		ctrlSecurityList.EgressSecurityRules = append(ctrlSecurityList.EgressSecurityRules, core.EgressSecurityRule{
			Protocol:    common.String("6"), // TCP
			Destination: common.String(bastionCIDRBlock),
			Description: common.String("All traffic to bastion"),
			IsStateless: common.Bool(false),
		})
	}

	ctrlSecurityList.IngressSecurityRules = append(ctrlSecurityList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol:    common.String("6"), // TCP
		Source:      common.String(nodeCidrBlock),
		Description: common.String("Kubernetes worker to control plane communication"),
		TcpOptions: &core.TcpOptions{
			DestinationPortRange: &core.PortRange{
				Max: common.Int(12250),
				Min: common.Int(12250),
			},
		},
		IsStateless: common.Bool(false),
	})

	// Default egress rule to allow outbound traffic to worker nodes
	ctrlSecurityList.EgressSecurityRules = append(ctrlSecurityList.EgressSecurityRules, core.EgressSecurityRule{
		Protocol:    common.String("6"), // TCP
		Destination: common.String(nodeCidrBlock),
		Description: common.String("All traffic to worker nodes"),
		IsStateless: common.Bool(false),
	})
	// Allow ICMP traffic from worker nodes for path discovery
	ctrlSecurityList.EgressSecurityRules = append(ctrlSecurityList.EgressSecurityRules, core.EgressSecurityRule{
		Protocol:    common.String("1"),
		Destination: common.String(nodeCidrBlock),
		Description: common.String("Path discovery"),
		IcmpOptions: &core.IcmpOptions{
			Code: common.Int(4),
			Type: common.Int(3),
		},
		IsStateless: common.Bool(false),
	})

	var allServices *core.Service
	serviceList, err := mgr.virtualNetworkClient.ListServices(ctx, core.ListServicesRequest{})
	if err != nil {
		return []string{}, err
	}
	for _, service := range serviceList.Items {
		if strings.HasSuffix(strings.ToLower(*service.Name), strings.ToLower("Services In Oracle Services Network")) {
			allServices = &service
			// Default egress rule to allow outbound traffic to OKE
			ctrlSecurityList.EgressSecurityRules = append(ctrlSecurityList.EgressSecurityRules, core.EgressSecurityRule{
				Protocol:        common.String("6"), // TCP
				Destination:     allServices.CidrBlock,
				DestinationType: core.EgressSecurityRuleDestinationTypeServiceCidrBlock,
				Description:     common.String("Allow Kubernetes Control Plane to communicate with OKE"),
				TcpOptions: &core.TcpOptions{
					DestinationPortRange: &core.PortRange{
						Max: common.Int(443),
						Min: common.Int(443),
					},
				},
				IsStateless: common.Bool(false),
			})
		}
	}

	ctrlSecurityListResp, err := mgr.virtualNetworkClient.CreateSecurityList(ctx, ctrlSecurityList)
	helpers.FatalIfError(err)

	return []string{*ctrlSecurityListResp.SecurityList.Id}, nil
}

// Create the service security list
func (mgr *ClusterManagerClient) CreateServiceSecurityList(ctx context.Context, state *State, vcnId *string, name string) ([]string, error) {

	// Allow incoming traffic on 80 and 443
	httpPortRange := core.PortRange{
		Max: common.Int(80),
		Min: common.Int(80),
	}
	httpsPortRange := core.PortRange{
		Max: common.Int(443),
		Min: common.Int(443),
	}
	// Allow incoming traffic to the Istio ingress gateway port
	istioGatewayPort := core.PortRange{
		Max: common.Int(15443),
		Min: common.Int(15443),
	}
	svcSecList := core.CreateSecurityListRequest{
		CreateSecurityListDetails: core.CreateSecurityListDetails{
			CompartmentId:        &state.CompartmentID,
			DisplayName:          common.String(name),
			EgressSecurityRules:  []core.EgressSecurityRule{},
			IngressSecurityRules: []core.IngressSecurityRule{},
			VcnId:                vcnId}}

	svcSecList.IngressSecurityRules = append(svcSecList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol: common.String("6"), // TCP
		Source:   common.String("0.0.0.0/0"),
		TcpOptions: &core.TcpOptions{
			DestinationPortRange: &httpPortRange,
		},
	})
	svcSecList.IngressSecurityRules = append(svcSecList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol: common.String("6"), // TCP
		Source:   common.String("0.0.0.0/0"),
		TcpOptions: &core.TcpOptions{
			DestinationPortRange: &httpsPortRange,
		},
	})
	svcSecList.IngressSecurityRules = append(svcSecList.IngressSecurityRules, core.IngressSecurityRule{
		Protocol: common.String("6"), // TCP
		Source:   common.String("0.0.0.0/0"),
		TcpOptions: &core.TcpOptions{
			DestinationPortRange: &istioGatewayPort,
		},
	})

	svcSecListResp, err := mgr.virtualNetworkClient.CreateSecurityList(ctx, svcSecList)
	helpers.FatalIfError(err)

	return []string{*svcSecListResp.SecurityList.Id}, nil
}

// getResourceID returns a resource ID based on the filter of resource actionType and entityType
func getResourceID(resources []containerengine.WorkRequestResource, actionType containerengine.WorkRequestResourceActionTypeEnum, entityType string) *string {

	for _, resource := range resources {
		if resource.ActionType == actionType && strings.ToUpper(*resource.EntityType) == entityType {
			return resource.Identifier
		}
	}

	return nil
}

// wait until work request finish
func waitUntilWorkRequestComplete(client containerengine.ContainerEngineClient, workRequestID *string) (containerengine.GetWorkRequestResponse, error) {
	// TODO - this function seems to be taking too long and not returning as
	//  soon as the job appears to be complete.

	if workRequestID == nil || len(*workRequestID) == 0 {
		return containerengine.GetWorkRequestResponse{}, fmt.Errorf("a valid workRequestID is required")
	}

	// retry GetWorkRequest call until operation is no longer waiting to start or in in-progress
	shouldRetryFunc := func(r common.OCIOperationResponse) bool {
		return r.Response.(containerengine.GetWorkRequestResponse).Status == containerengine.WorkRequestStatusInProgress ||
			r.Response.(containerengine.GetWorkRequestResponse).Status == containerengine.WorkRequestStatusAccepted
	}

	getWorkReq := containerengine.GetWorkRequestRequest{
		WorkRequestId:   workRequestID,
		RequestMetadata: helpers.GetRequestMetadataWithCustomizedRetryPolicy(shouldRetryFunc),
	}

	getResp, err := client.GetWorkRequest(context.Background(), getWorkReq)
	if err != nil {
		return getResp, err
	}

	return getResp, nil
}

// limitN returns n if n is less than a non-zero limit, or the limit l if n is greater.
func limitN(n, l int) int {
	if l > 0 && n > l {
		return l
	}
	return n
}

// numADs returns the number of availability domains in the current context.
func (mgr *ClusterManagerClient) numADs(ctx context.Context, compartmentID string) int {

	req := identity.ListAvailabilityDomainsRequest{}
	req.CompartmentId = &compartmentID
	ads, err := mgr.identityClient.ListAvailabilityDomains(ctx, req)
	if err != nil {
		return 0
	}
	return len(ads.Items)
}

func getDefaultKubernetesVersion(client containerengine.ContainerEngineClient) (*string, error) {

	getClusterOptionsReq := containerengine.GetClusterOptionsRequest{
		ClusterOptionId: common.String("all"),
	}
	getClusterOptionsResp, err := client.GetClusterOptions(context.Background(), getClusterOptionsReq)
	if err != nil {
		return nil, err
	}

	kubernetesVersion := getClusterOptionsResp.KubernetesVersions

	if len(kubernetesVersion) < 1 {
		return nil, fmt.Errorf("no Kubernetes versions are available")
	}

	// TODO assuming the last item in the list is the latest version.
	return &kubernetesVersion[len(kubernetesVersion)-1], nil
}

type SignRequest func(*http.Request) (*http.Request, error)

// generateToken generates a v2 token using the signer and cluster id similar to what
// oci ce cluster generate-token --cluster-id does.
func generateToken(sign SignRequest, region string, clusterID string) (string, error) {
	requiredHeaders := []string{"date", "authorization"}

	endpoint := fmt.Sprintf("https://containerengine.%s.oraclecloud.com/cluster_request/%s", region, clusterID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	req, err = sign(req)
	if err != nil {
		return "", err
	}

	url := req.URL
	query := url.Query()
	for _, header := range requiredHeaders {
		query.Set(header, req.Header.Get(header))
	}

	url.RawQuery = query.Encode()

	return base64.URLEncoding.EncodeToString([]byte(url.String())), nil
}

func newTokenSigner(requestSigner common.HTTPRequestSigner, interceptor common.RequestInterceptor) SignRequest {
	return func(r *http.Request) (*http.Request, error) {
		r.Header.Set("date", time.Now().UTC().Format(http.TimeFormat))
		r.Header.Set("user-agent", "OKERancherDriver")

		err := interceptor(r)
		if err != nil {
			return nil, err
		}

		err = requestSigner.Sign(r)
		if err != nil {
			return nil, err
		}

		return r, nil
	}
}

func negate(b bool) *bool {
	if b {
		return common.Bool(false)
	}
	return common.Bool(true)
}
