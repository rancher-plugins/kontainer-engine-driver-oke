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
* This is the main driver for the Rancher plug-in to provide CRUD operations for Oracle Container Engine.
*  The driver implements interface required by Rancher (Create, Update, Remove, Get*, etc).
 */

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/oracle/oci-go-sdk/common"
	"github.com/pkg/errors"
	"github.com/rancher/kontainer-engine/drivers/options"
	"github.com/rancher/kontainer-engine/types"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultNumNodes            = 0
	defaultVCNNamePrefix       = "oke-"
	defaultServiceSubnetPrefix = "oke-"
)

type OKEDriver struct {
	driverCapabilities types.Capabilities
}

type State struct {
	// Should the Kubernetes dashboard be enabled
	EnableKubernetesDashboard bool

	// Should the Helm server (Tiller) be enabled
	EnableTiller bool

	PrivateNodes bool

	// The name of the cluster (and default node pool)
	Name string

	// The Oracle Cloud ID (OCID) of the tenancy
	TenancyID string

	// The OCID of the cluster compartment
	CompartmentID string

	// The user OCID
	UserOCID string

	// The path to the private API Key that is associated with the user and has access the tenancy/compartment
	PrivateKeyPath string
	// The contents the private API Key that is associated with the user and has access the tenancy/compartment
	PrivateKeyContents string

	// The API Key Fingerprint
	Fingerprint string

	// The region where the cluster will be hosted
	Region string

	// The passphrase for the private key
	PrivateKeyPassphrase string

	// The description of the cluster
	// TODO currently unused
	Description string

	// Should cluster creation operation wait until nodes are active
	// TODO currently unused
	WaitNodesActive int64

	// Optional CIDR from which to allow ingress to worker nodes
	WorkerNodeIngressCidr string

	// The labels specified during the Kubernetes creation
	// TODO currently unused
	KubernetesLabels map[string]string

	// The version of Kubernetes to run on the master and worker nodes and node pool (e.g. v1.11.9, v1.12.7)
	KubernetesVersion string

	// OCID of the cluster
	ClusterID string

	Network  NetworkConfiguration
	NodePool NodeConfiguration
	// cluster info
	ClusterInfo types.ClusterInfo

	// Should the Deletion of VCN be skipped
	SkipVCNDelete bool
}

// Elements that make up the Network configuration (and state) for the OKE cluster
type NetworkConfiguration struct {
	// The OCID of the compartment that contains the optional pre-existing VCN
	VcnCompartmentID string
	// Optional pre-existing VCN in which you want to create cluster
	VCNName string
	// Optional pre-existing load balancer subnets to host load balancers for services
	ServiceLBSubnet1Name string
	ServiceLBSubnet2Name string
	// The number of AD specific subnets (each are created in different availability domains)
	QuantityOfSubnets int64
	// Optional name of node pool subnet
	NodePoolSubnetName string
	// Optional name of node pool subnet security list
	NodePoolSubnetSecurityListName string
	// Optional name of node pool dns domain name
	NodePoolSubnetDnsDomainName string
	// Optional name of the service subnet security list
	ServiceSubnetSecurityListName string
	// Optional name of the service subnet dns domain name
	ServiceSubnetDnsDomainName string
}

// Elements that make up the configuration of each node in the OKE cluster
type NodeConfiguration struct {
	// The OS image that will be used for the VM
	NodeImageName string
	// The shape of the VM for the worker node
	NodeShape string
	// The optional public SSH Key path to access the worker nodes
	// Note, in order to access private nodes you need to set up a bastion host on the bastion subnet
	NodePublicSSHKeyPath string
	// The optional public SSH Key contents to access the worker nodes
	NodePublicSSHKeyContents string
	// The number of nodes in each subnet / availability domain
	QuantityPerSubnet int64
}

func NewDriver() types.Driver {
	driver := &OKEDriver{
		driverCapabilities: types.Capabilities{
			Capabilities: make(map[int64]bool),
		},
	}

	driver.driverCapabilities.AddCapability(types.GetVersionCapability)
	driver.driverCapabilities.AddCapability(types.SetVersionCapability)
	driver.driverCapabilities.AddCapability(types.GetClusterSizeCapability)
	driver.driverCapabilities.AddCapability(types.SetClusterSizeCapability)

	return driver
}

func (d *OKEDriver) Remove(ctx context.Context, info *types.ClusterInfo) error {
	logrus.Debugf("oke.driver.Remove(...) called")
	// Delete the cluster along with its node-pools and VCN (and associated network resource)

	state, err := GetState(info)
	if err != nil {
		return err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return errors.Wrap(err, "could not get container engine client")
	}

	nodePoolIDs, err := oke.ListNodepoolIdsInCluster(ctx, state.CompartmentID, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not retrieve node pool id")
	}

	for _, nodePoolID := range nodePoolIDs {
		logrus.Infof("Deleting node pool %s", nodePoolID)
		err := oke.DeleteNodePool(ctx, nodePoolID)
		if err != nil {
			return errors.Wrap(err, "could not delete node pool")
		}
	}

	// Get the VCN ID before the cluster is DELETED
	vcnID, err := oke.GetVcnIDByClusterID(ctx, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not retrieve virtual cloud network (VCN)")
	}

	logrus.Info("Deleting cluster")
	err = oke.DeleteCluster(ctx, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not delete cluster")
	}

	if state.SkipVCNDelete {
		return nil
	}

	// TODO we could be deleting a preexisting VCN here
	logrus.Info("Deleting VCN")
	err = oke.DeleteVCN(ctx, vcnID)
	if err != nil {
		return errors.Wrap(err, "could not delete virtual cloud network (VCN)")
	}
	return nil
}

func GetState(info *types.ClusterInfo) (State, error) {
	logrus.Debugf("oke.driver.GetState(...) called")
	state := State{}
	err := json.Unmarshal([]byte(info.Metadata["state"]), &state)
	return state, err
}

// GetDriverCreateOptions implements driver interface
func (d *OKEDriver) GetDriverCreateOptions(ctx context.Context) (*types.DriverFlags, error) {
	logrus.Debugf("oke.driver.GetDriverCreateOptions(...) called")

	driverFlag := types.DriverFlags{
		Options: make(map[string]*types.Flag),
	}
	driverFlag.Options["name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The internal name of the Oracle Container Engine (OKE) cluster in Rancher",
	}
	driverFlag.Options["user-ocid"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The OCID of a user who has access to the tenancy/compartment",
	}
	driverFlag.Options["tenancy-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The tenancy name for the OKE cluster",
	}
	driverFlag.Options["tenancy-id"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The OCID of the tenancy in which to create resources",
	}
	driverFlag.Options["fingerprint"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The fingerprint corresponding to the specified user's private API Key",
	}
	driverFlag.Options["private-key-path"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The private API key path for the specified user, in PEM format",
	}
	driverFlag.Options["private-key-contents"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The private API key contents for the specified user, in PEM format",
	}
	driverFlag.Options["private-key-passphrase"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The passphrase of the private key for the OKE cluster",
	}
	driverFlag.Options["region"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The region where the OKE cluster will be hosted",
	}
	driverFlag.Options["availability-domain"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The availability domain within the region to host the OKE cluster",
	}
	driverFlag.Options["display-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The display name of the OKE cluster (and VCN and node pool if applicable) that should be displayed to the user",
	}
	driverFlag.Options["kubernetes-version"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The Kubernetes version that will be used for your master and worker nodes e.g. v1.11.9, v1.12.7",
	}
	driverFlag.Options["enable-kubernetes-dashboard"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Enable the kubernetes dashboard",
	}
	driverFlag.Options["enable-tiller"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Enable the kubernetes dashboard",
	}
	driverFlag.Options["skip-vcn-delete"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Whether to skip deleting VCN",
	}
	driverFlag.Options["node-public-key-path"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The path to the SSH public key to use for the nodes",
	}
	driverFlag.Options["node-public-key-contents"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The contents of the SSH public key to use for the nodes",
	}
	driverFlag.Options["quantity-of-node-subnets"] = &types.Flag{
		Type:  types.IntType,
		Usage: "Number of node subnets (defaults to one in each AD)",
	}
	driverFlag.Options["quantity-per-subnet"] = &types.Flag{
		Type:  types.IntType,
		Usage: "Number of worker nodes in each subnet / availability domain",
		Default: &types.Default{
			DefaultInt: 1,
		},
	}
	driverFlag.Options["wait-nodes-active"] = &types.Flag{
		Type:  types.IntType,
		Usage: "Whether to wait for the nodes to reach READY state before moving on",
	}
	driverFlag.Options["kubernetes-label"] = &types.Flag{
		Type:  types.StringSliceType,
		Usage: "The Kubernetes labels for the OKE cluster",
	}
	driverFlag.Options["node-image"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The OS for the node image",
	}
	driverFlag.Options["node-shape"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The shape of the node (determines number of CPUs and  amount of memory on each node)",
	}
	driverFlag.Options["compartment-id"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The OCID of the compartment in which to create resrouces (VCN, worker nodes, etc.)",
	}
	driverFlag.Options["vcn-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of an existing virtual network to be used for OKE cluster creation",
	}
	driverFlag.Options["load-balancer-subnet-name-1"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of the first existing subnet to use for Kubernetes services / LB",
	}
	driverFlag.Options["load-balancer-subnet-name-2"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of the second existing subnet to use for Kubernetes services / LB",
	}
	driverFlag.Options["enable-private-nodes"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Whether worker nodes are deployed in private subnets",
	}
	driverFlag.Options["worker-node-ingress-cidr"] = &types.Flag{
		Type:  types.StringType,
		Usage: "Additional CIDR from which to allow ingress to worker nodes",
	}
	driverFlag.Options["node-pool-subnet-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "Optional name for node pool subnet",
		Default: &types.Default{
			DefaultString: nodeSubnetName,
		},
	}
	driverFlag.Options["node-pool-security-list-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "Optional name for security list of node pool subnet",
		Default: &types.Default{
			DefaultString: nodePoolSubnetSecurityListName,
		},
	}
	driverFlag.Options["node-pool-dns-domain-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "Optional name for DNS domain of node pool subnet",
		Default: &types.Default{
			DefaultString: nodeSubnetName,
		},
	}
	driverFlag.Options["service-security-list-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "Optional name for security list of service subnet",
		Default: &types.Default{
			DefaultString: serviceSubnetSecurityListName,
		},
	}
	driverFlag.Options["service-dns-domain-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "Optional name for DNS domain of service subnet",
		Default: &types.Default{
			DefaultString: serviceSubnetName,
		},
	}

	return &driverFlag, nil
}

// GetDriverUpdateOptions implements driver interface
func (d *OKEDriver) GetDriverUpdateOptions(ctx context.Context) (*types.DriverFlags, error) {
	logrus.Debugf("oke.driver.GetDriverUpdateOptions(...) called")

	driverFlag := types.DriverFlags{
		Options: make(map[string]*types.Flag),
	}
	driverFlag.Options["quantity-per-subnet"] = &types.Flag{
		Type:  types.IntType,
		Usage: "The updated number of worker nodes in each subnet to update. 1 (default) means no updates",
	}
	driverFlag.Options["kubernetes-version"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The updated Kubernetes version",
	}
	return &driverFlag, nil
}

// SetDriverOptions implements driver interface
func GetStateFromOpts(driverOptions *types.DriverOptions) (State, error) {
	logrus.Debugf("oke.driver.GetStateFromOpts(...) called")

	// Capture the requested options for the cluster
	state := State{
		ClusterInfo: types.ClusterInfo{
			Metadata: map[string]string{},
		},
		KubernetesLabels: map[string]string{},
	}

	state.CompartmentID = options.GetValueFromDriverOptions(driverOptions, types.StringType, "compartment-id", "compartmentId").(string)
	state.Description = options.GetValueFromDriverOptions(driverOptions, types.StringType, "description").(string)
	state.EnableKubernetesDashboard = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "enable-kubernetes-dashboard", "enableKubernetesDashboard").(bool)
	state.EnableTiller = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "enable-tiller", "enableTiller").(bool)
	state.SkipVCNDelete = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "skip-vcn-delete", "skipVCNDelete").(bool)
	state.Fingerprint = options.GetValueFromDriverOptions(driverOptions, types.StringType, "fingerprint", "fingerprint").(string)
	state.KubernetesVersion = options.GetValueFromDriverOptions(driverOptions, types.StringType, "kubernetes-version", "kubernetesVersion").(string)
	state.Name = options.GetValueFromDriverOptions(driverOptions, types.StringType, "name").(string)
	state.PrivateKeyContents = options.GetValueFromDriverOptions(driverOptions, types.StringType, "private-key-contents", "privateKeyContents").(string)
	state.PrivateKeyPath = options.GetValueFromDriverOptions(driverOptions, types.StringType, "private-key-path", "privateKeyPath").(string)
	state.PrivateKeyPassphrase = options.GetValueFromDriverOptions(driverOptions, types.StringType, "private-key-passphrase", "privateKeyPassphrase").(string)
	state.Region = options.GetValueFromDriverOptions(driverOptions, types.StringType, "region").(string)
	state.TenancyID = options.GetValueFromDriverOptions(driverOptions, types.StringType, "tenancy-id", "tenancyId").(string)
	state.UserOCID = options.GetValueFromDriverOptions(driverOptions, types.StringType, "user-ocid", "userOcid").(string)
	state.WaitNodesActive = options.GetValueFromDriverOptions(driverOptions, types.IntType, "wait-nodes-active", "waitNodesActive").(int64)
	state.PrivateNodes = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "enable-private-nodes", "enablePrivateNodes").(bool)
	state.WorkerNodeIngressCidr = options.GetValueFromDriverOptions(driverOptions, types.StringType, "worker-node-ingress-cidr", "WorkerNodeIngressCidr", "workerNodeIngressCidr").(string)

	state.NodePool = NodeConfiguration{
		NodeImageName:            options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-image", "nodeImage").(string),
		NodeShape:                options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-shape", "nodeShape").(string),
		NodePublicSSHKeyPath:     options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-public-key-path", "nodePublicKeyPath").(string),
		NodePublicSSHKeyContents: options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-public-key-contents", "nodePublicKeyContents").(string),
		QuantityPerSubnet:        options.GetValueFromDriverOptions(driverOptions, types.IntType, "quantity-per-subnet", "quantityPerSubnet").(int64),
	}

	state.Network = NetworkConfiguration{
		VcnCompartmentID:               options.GetValueFromDriverOptions(driverOptions, types.StringType, "vcn-compartment-id", "vcnCompartmentId").(string),
		VCNName:                        options.GetValueFromDriverOptions(driverOptions, types.StringType, "vcn-name", "vcnName").(string),
		ServiceLBSubnet1Name:           options.GetValueFromDriverOptions(driverOptions, types.StringType, "load-balancer-subnet-name-1", "loadBalancerSubnetName1").(string),
		ServiceLBSubnet2Name:           options.GetValueFromDriverOptions(driverOptions, types.StringType, "load-balancer-subnet-name-2", "loadBalancerSubnetName2").(string),
		QuantityOfSubnets:              options.GetValueFromDriverOptions(driverOptions, types.IntType, "quantity-of-node-subnets", "quantityOfNodeSubnets").(int64),
		NodePoolSubnetName:             options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-pool-subnet-name", "nodePoolSubnetName").(string),
		NodePoolSubnetSecurityListName: options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-pool-subnet-security-list-name", "nodePoolSubnetSecurityListName").(string),
		NodePoolSubnetDnsDomainName:    options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-pool-dns-domain-list-name", "nodePoolSubnetDnsDomainName").(string),
		ServiceSubnetSecurityListName:  options.GetValueFromDriverOptions(driverOptions, types.StringType, "service-subnet-security-list-name", "serviceSubnetSecurityListName").(string),
		ServiceSubnetDnsDomainName:     options.GetValueFromDriverOptions(driverOptions, types.StringType, "service-subnet-dns-domain-name", "serviceSubnetDnsDomainName").(string),
	}

	if state.Network.NodePoolSubnetName == "" {
		state.Network.NodePoolSubnetName = nodeSubnetName
	}

	if state.Network.NodePoolSubnetSecurityListName == "" {
		state.Network.NodePoolSubnetSecurityListName = nodePoolSubnetSecurityListName
	}

	if state.Network.NodePoolSubnetDnsDomainName == "" {
		state.Network.NodePoolSubnetDnsDomainName = nodeSubnetName
	}

	if state.Network.ServiceSubnetSecurityListName == "" {
		state.Network.ServiceSubnetSecurityListName = serviceSubnetSecurityListName
	}

	if state.Network.ServiceSubnetDnsDomainName == "" {
		state.Network.ServiceSubnetDnsDomainName = serviceSubnetName
	}

	if state.NodePool.QuantityPerSubnet == 0 {
		state.NodePool.QuantityPerSubnet = defaultNumNodes
	}

	if state.Network.VcnCompartmentID == "" {
		state.Network.VcnCompartmentID = state.CompartmentID
	}

	if state.PrivateKeyContents == "" && state.PrivateKeyPath != "" {
		privateKeyBytes, err := ioutil.ReadFile(state.PrivateKeyPath)
		if err == nil {
			state.PrivateKeyContents = string(privateKeyBytes)
		}
	}

	if state.NodePool.NodePublicSSHKeyContents == "" && state.NodePool.NodePublicSSHKeyPath != "" {
		publicKeyBytes, err := ioutil.ReadFile(state.NodePool.NodePublicSSHKeyPath)
		if err == nil {
			state.NodePool.NodePublicSSHKeyContents = string(publicKeyBytes)
		}
	}

	return state, state.validate()
}

func (s *State) validate() error {
	logrus.Debugf("oke.driver.validate(...) called")
	if s.PrivateKeyPath == "" && s.PrivateKeyContents == "" {
		return fmt.Errorf(`"private-key-path or private-key-contents" are required`)
	} else if s.TenancyID == "" {
		return fmt.Errorf(`"tenancy-id" is required`)
	} else if s.UserOCID == "" {
		return fmt.Errorf(`"user-ocid" is required`)
	} else if s.Fingerprint == "" {
		return fmt.Errorf(`"fingerprint" is required`)
	} else if s.CompartmentID == "" {
		return fmt.Errorf(`"compartment-id" is required`)
	} else if s.Region == "" {
		return fmt.Errorf(`"region" is required`)
	} else if s.NodePool.NodeImageName == "" {
		return fmt.Errorf(`"node-image " is required`)
	} else if s.NodePool.NodeShape == "" {
		return fmt.Errorf(`"node-shape " is required`)
	} else if s.Network.VCNName != "" && (s.Network.ServiceLBSubnet1Name == "") {
		return fmt.Errorf(`"vcn-name" and "load-balancer-subnet-name-1" must be set together"`)
	}

	return nil
}

// Create implements driver interface
func (d *OKEDriver) Create(ctx context.Context, opts *types.DriverOptions, _ *types.ClusterInfo) (*types.ClusterInfo, error) {
	logrus.Debugf("oke.driver.Create(...) called")

	state, err := GetStateFromOpts(opts)
	if err != nil {
		return nil, err
	}

	/*
	* The ClusterInfo includes the following information Version, ServiceAccountToken,Endpoint, username, password, etc
	 */
	clusterInfo := &types.ClusterInfo{}
	err = storeState(clusterInfo, state)
	if err != nil {
		logrus.Debugf("error storing state %v", err)
		return clusterInfo, err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return clusterInfo, err
	}

	var vcnID string
	var serviceSubnetIDs, nodeSubnetIds []string

	// Check if we should create a new VCN or make use of an existing VCN and subnets.
	if state.Network.VCNName == "" {
		// Create a new Virtual Cloud Network with the default number of subnets
		state.Network.VCNName = defaultVCNNamePrefix + state.Name
		state.Network.ServiceLBSubnet1Name = defaultServiceSubnetPrefix + state.Name

		logrus.Infof("Creating a new VCN and required network resources for OKE cluster %s", state.Name)
		vcnID, serviceSubnetIDs, nodeSubnetIds, err = oke.CreateVCNAndNetworkResources(&state)

		if err != nil {
			logrus.Debugf("error creating the VCN and/or the required network resources %v", err)
			return clusterInfo, err
		}

	} else {
		// Use an existing VCN and subnets. Besides the VCN and subnets, we are
		// assuming that the internet gateway, route table, security lists are present and configured correctly.
		logrus.Debugf("using an existing VCN %s", state.Network.VCNName)
		vcnID, err = oke.GetVcnIDByName(ctx, state.Network.VcnCompartmentID, state.Network.VCNName)
		if err != nil {
			logrus.Debugf("error looking up the Id of existing VCN %s %v", state.Network.VCNName, err)
			return clusterInfo, err
		}
		serviceSubnet1Id, err := oke.GetSubnetIDByName(ctx, state.Network.VcnCompartmentID, vcnID, state.Network.ServiceLBSubnet1Name)
		if err != nil {
			logrus.Debugf("error looking up the Id of a Kubernetes service Subnet %s %v", state.Network.ServiceLBSubnet1Name, err)
			return clusterInfo, err
		}
		serviceSubnetIDs = append(serviceSubnetIDs, serviceSubnet1Id)

		if state.Network.ServiceLBSubnet2Name != "" {
			serviceSubnet2Id, err := oke.GetSubnetIDByName(ctx, state.Network.VcnCompartmentID, vcnID, state.Network.ServiceLBSubnet2Name)
			if err != nil {
				logrus.Debugf("error looking up the Id of a Kubernetes service Subnet %s %v", state.Network.ServiceLBSubnet2Name, err)
				return clusterInfo, err
			}
			serviceSubnetIDs = append(serviceSubnetIDs, serviceSubnet2Id)
		}

		// First, get all the subnet Ids in the VCN, then remove the service subnets which should leave the node subnets
		nodeSubnetIds, _ = oke.ListSubnetIdsInVcn(ctx, state.Network.VcnCompartmentID, vcnID)
		for _, serviceSubnetID := range serviceSubnetIDs {
			for i, vcnSubnetID := range nodeSubnetIds {
				if serviceSubnetID == vcnSubnetID {
					nodeSubnetIds = remove(nodeSubnetIds, i)
				}
			}
		}

		// When using an existing VCN, we require at least one subnet (preferably regional) for node pool.
		if len(nodeSubnetIds) < 1 {
			return clusterInfo, fmt.Errorf("VCN must have at least 1 node subnet for node pool")
		}

		state.Network.QuantityOfSubnets = int64(len(nodeSubnetIds))
	}

	clusterID, err := oke.GetClusterByName(ctx, state.CompartmentID, state.Name)
	if err == nil && len(clusterID) > 0 {
		logrus.Debugf("warning: an existing cluster with name %s already exists in compartment %s", state.Name, state.CompartmentID)
	}

	logrus.Infof("Creating OKE cluster %s", state.Name)
	err = oke.CreateCluster(ctx, &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	if err != nil {
		logrus.Debugf("error creating the OKE cluster %v", err)
		return clusterInfo, err
	}
	err = storeState(clusterInfo, state)
	if err != nil {
		logrus.Debugf("error storing state %v", err)
		return clusterInfo, err
	}

	logrus.Infof("Creating node pool for %s", state.Name)
	err = oke.CreateNodePool(ctx, &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	if err != nil {
		logrus.Debugf("error creating the node pool %v", err)
		return clusterInfo, err
	}

	err = storeState(clusterInfo, state)
	if err != nil {
		logrus.Debugf("error storing state %v", err)
		return clusterInfo, err
	}

	return clusterInfo, nil
}

// Update implements driver interface
func (d *OKEDriver) Update(ctx context.Context, info *types.ClusterInfo, opts *types.DriverOptions) (*types.ClusterInfo, error) {
	logrus.Debugf("oke.driver.Update(...) called")
	state, err := GetState(info)
	if err != nil {
		return nil, err
	}

	newState, err := GetStateFromOpts(opts)
	if err != nil {
		return nil, err
	}

	if newState.NodePool.QuantityPerSubnet != state.NodePool.QuantityPerSubnet {
		logrus.Infof("Updating quantity of nodes per subnet to %d", uint64(newState.NodePool.QuantityPerSubnet))
		err = d.SetClusterSize(ctx, info, &types.NodeCount{Count: int64(newState.NodePool.QuantityPerSubnet) * int64(state.Network.QuantityOfSubnets)})
		if err != nil {
			return nil, err
		}
		state.NodePool.QuantityPerSubnet = newState.NodePool.QuantityPerSubnet
	}
	if newState.KubernetesVersion != state.KubernetesVersion {
		logrus.Infof("Updating Kubernetes version to %s", newState.KubernetesVersion)
		err = d.SetVersion(ctx, info, &types.KubernetesVersion{Version: string(newState.KubernetesVersion)})
		if err != nil {
			return nil, err
		}
		state.KubernetesVersion = newState.KubernetesVersion
	}

	logrus.Info("Cluster updates may continue asynchronously")
	return info, storeState(info, state)
}

func (d *OKEDriver) PostCheck(ctx context.Context, info *types.ClusterInfo) (*types.ClusterInfo, error) {
	logrus.Debugf("oke.driver.PostCheck(...) called")
	state, err := GetState(info)
	if err != nil {
		return nil, err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return nil, errors.Wrap(err, "could not get Oracle Container Engine client")
	}

	cluster, err := oke.GetClusterByID(ctx, state.ClusterID)
	if err != nil {
		return nil, errors.Wrap(err, "could not retrieve the cluster")
	}

	kubeConfig, _, err := oke.GetKubeconfigByClusterID(ctx, state.ClusterID, state.Region)
	if err != nil {
		return nil, errors.Wrap(err, "could not retrieve the Oracle Container Engine kubeconfig")
	}

	info.Endpoint = *cluster.Endpoints.Kubernetes
	info.Version = *cluster.KubernetesVersion
	info.Username = ""
	info.Password = ""
	info.ClientCertificate = ""
	info.ClientKey = ""
	info.NodeCount = state.NodePool.QuantityPerSubnet * state.Network.QuantityOfSubnets
	info.Metadata["nodePool"] = state.Name + "-1"
	if len(kubeConfig.Clusters) > 0 {
		info.RootCaCertificate = kubeConfig.Clusters[0].Cluster.CertificateAuthorityData
	}

	// Use as a temporary token while we generate a service account.
	if len(kubeConfig.Users) > 0 {
		if kubeConfig.Users[0].User.Token != "" {
			info.ServiceAccountToken = kubeConfig.Users[0].User.Token
		}
		// TODO handle info.ExecCredential when it is supported by Rancher
		// https://github.com/rancher/rancher/issues/24135
	}

	kubeConfigStr, err := yaml.Marshal(&kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("error marshaling kubeconfig: %v", err)
	}

	clientSet, err := getClientsetFromKubeconfig(kubeConfigStr)
	if err != nil {
		return nil, fmt.Errorf("error creating clientset from kubeconfig: %v", err)
	}

	info.ServiceAccountToken, err = generateServiceAccountToken(clientSet)
	if err != nil {
		return nil, errors.Wrap(err, "could not generate service account token from OKE kubeconfig")
	}

	return info, nil
}

func (d *OKEDriver) GetClusterSize(ctx context.Context, info *types.ClusterInfo) (*types.NodeCount, error) {
	logrus.Debugf("oke.driver.GetClusterSize(...) called")
	state, err := GetState(info)
	if err != nil {
		return nil, err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return nil, errors.Wrap(err, "could not get Oracle Container Engine client")
	}

	nodePoolIds, err := oke.ListNodepoolIdsInCluster(ctx, state.CompartmentID, state.ClusterID)
	if err != nil {
		return nil, errors.Wrap(err, "could not retrieve node pool id")
	}

	if len(nodePoolIds) <= 0 {
		return nil, fmt.Errorf("could not retrieve node pool id")
	}

	// Assumption of a single node pool here
	nodePool, err := oke.GetNodePoolByID(ctx, nodePoolIds[0])
	if err != nil {
		return nil, errors.Wrap(err, "could not retrieve node pool")
	}

	nodeCount := &types.NodeCount{Count: int64(len(nodePool.Nodes))}

	return nodeCount, nil
}

/*
* Marshal the Oracle Container Engine configuration state and store it in the types.ClusterInfo
 */
func storeState(info *types.ClusterInfo, state State) error {
	bytes, err := json.Marshal(state)
	if err != nil {
		return errors.Wrap(err, "could not marshal state")
	}

	if info.Metadata == nil {
		info.Metadata = map[string]string{}
	}
	info.Metadata["state"] = string(bytes)
	return nil
}

func (d *OKEDriver) GetVersion(ctx context.Context, info *types.ClusterInfo) (*types.KubernetesVersion, error) {
	logrus.Debugf("oke.driver.GetVersion(...) called")
	state, err := GetState(info)
	if err != nil {
		return nil, err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return nil, errors.Wrap(err, "could not get Oracle Container Engine client")
	}

	cluster, err := oke.GetClusterByID(ctx, state.ClusterID)
	if err != nil {
		return nil, errors.Wrap(err, "could not retrieve the Oracle Container Engine cluster")
	}

	version := &types.KubernetesVersion{Version: *cluster.KubernetesVersion}

	return version, nil
}

func (d *OKEDriver) SetClusterSize(ctx context.Context, info *types.ClusterInfo, count *types.NodeCount) error {
	logrus.Debugf("oke.driver.SetClusterSize(...) called")
	state, err := GetState(info)
	if err != nil {
		return err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return errors.Wrap(err, "could not get Oracle Container Engine client")
	}

	if count.Count%state.Network.QuantityOfSubnets != 0 {
		return fmt.Errorf("you must scale up/down node(s) in each subnet i.e. cluster-size must be an even multiple of %d", uint64(state.Network.QuantityOfSubnets))
	}

	desiredQtyPerSubnet := int(count.Count / state.Network.QuantityOfSubnets)

	logrus.Infof("Updating the number of nodes in each subnet to %d", desiredQtyPerSubnet)
	nodePoolIds, err := oke.ListNodepoolIdsInCluster(ctx, state.CompartmentID, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not retrieve node pool id")
	}
	if len(nodePoolIds) <= 0 {
		return fmt.Errorf("could not retrieve node pool id")
	}

	// Assuming a single node pool
	nodePool, err := oke.GetNodePoolByID(ctx, nodePoolIds[0])
	if err != nil {
		return errors.Wrap(err, "could not retrieve node pool")
	}
	if *nodePool.QuantityPerSubnet == desiredQtyPerSubnet {
		logrus.Infof("OKE cluster size is already %d (i.e. %d node(s) per subnet)", uint64(count.Count), desiredQtyPerSubnet)
		return nil
	}

	if *nodePool.QuantityPerSubnet == desiredQtyPerSubnet {
		logrus.Infof("OKE cluster size is already %d (i.e. %d node(s) per subnet)", uint64(count.Count), desiredQtyPerSubnet)
		return nil
	}

	err = oke.ScaleNodePool(ctx, nodePoolIds[0], desiredQtyPerSubnet)
	if err != nil {
		return errors.Wrap(err, "could not adjust the number of nodes in the pool")
	}

	// scale is currently asynchronous
	logrus.Info("Cluster size updated successfully")
	return nil
}

func (d *OKEDriver) SetVersion(ctx context.Context, info *types.ClusterInfo, version *types.KubernetesVersion) error {
	logrus.Debugf("oke.driver.SetVersion(...) called")

	state, err := GetState(info)
	if err != nil {
		return err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return errors.Wrap(err, "could not get Oracle Container Engine client")
	}
	// Does not currently wait
	mErr := oke.UpdateMasterKubernetesVersion(ctx, state.ClusterID, version.Version)
	if mErr != nil {
		logrus.Debugf("warning: could not update the version of Kubernetes master(s)")
	}

	nodePoolIds, err := oke.ListNodepoolIdsInCluster(ctx, state.CompartmentID, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not retrieve node pool id")
	}

	var npErr error
	for _, nodePoolID := range nodePoolIds {
		nodePool, err := oke.GetNodePoolByID(ctx, nodePoolID)
		if err != nil {
			logrus.Debugf("could not retrieve node pool")
			continue
		}
		logrus.Infof("Updating the version of Kubernetes to %s", version.Version)
		nextNpErr := oke.UpdateNodepoolKubernetesVersion(ctx, *nodePool.Id, version.Version)
		if nextNpErr != nil {
			npErr = nextNpErr
			logrus.Debugf("warning: could not update the version of Kubernetes master(s)")
		}
	}

	if mErr != nil {
		return errors.Wrap(mErr, "could not update the version of Kubernetes master(s)")
	} else if npErr != nil {
		return errors.Wrap(npErr, "could not update the Kubernetes version of one the node pool")
	}

	// update version is currently asynchronous
	logrus.Info("Cluster version (masters and node pools) updated successfully")
	return nil
}

func (d *OKEDriver) GetCapabilities(ctx context.Context) (*types.Capabilities, error) {
	logrus.Debugf("oke.driver.GetCapabilities(...) called")
	return &d.driverCapabilities, nil
}

func (d *OKEDriver) ETCDSave(ctx context.Context, clusterInfo *types.ClusterInfo, opts *types.DriverOptions, snapshotName string) error {
	logrus.Debugf("oke.driver.ETCDSave(...) called")
	return fmt.Errorf("ETCD backup operations are not implemented")
}

func (d *OKEDriver) ETCDRestore(ctx context.Context, clusterInfo *types.ClusterInfo, opts *types.DriverOptions, snapshotName string) (*types.ClusterInfo, error) {
	logrus.Debugf("oke.driver.ETCDRestore(...) called")
	return nil, fmt.Errorf("ETCD backup operations are not implemented")
}

func (d *OKEDriver) ETCDRemoveSnapshot(ctx context.Context, clusterInfo *types.ClusterInfo, opts *types.DriverOptions, snapshotName string) error {
	logrus.Debugf("oke.driver.ETCDRemoveSnapshot(...) called")
	return fmt.Errorf("ETCD backup operations are not implemented")
}

func (d *OKEDriver) GetK8SCapabilities(ctx context.Context, options *types.DriverOptions) (*types.K8SCapabilities, error) {
	logrus.Debugf("oke.driver.GetK8SCapabilities(...) called")

	// TODO OCI supports persistent volumes as well
	capabilities := &types.K8SCapabilities{
		L4LoadBalancer: &types.LoadBalancerCapabilities{
			Enabled:              true,
			Provider:             "OCILB",
			ProtocolsSupported:   []string{"TCP", "HTTP/1.0", "HTTP/1.1"},
			HealthCheckSupported: true,
		},
	}

	return capabilities, nil
}

func (d *OKEDriver) RemoveLegacyServiceAccount(ctx context.Context, info *types.ClusterInfo) error {
	logrus.Debugf("oke.driver.RemoveLegacyServiceAccount(...) called")
	// TODO
	return nil
}

// constructClusterManagerClient is a helper function that constructs a new
// NewClusterManagerClient based on the state.
func constructClusterManagerClient(ctx context.Context, state State) (ClusterManagerClient, error) {
	configurationProvider := common.NewRawConfigurationProvider(
		state.TenancyID,
		state.UserOCID,
		state.Region,
		state.Fingerprint,
		state.PrivateKeyContents,
		&state.PrivateKeyPassphrase)

	clusterMgrClient, err := NewClusterManagerClient(configurationProvider)
	if err != nil {
		return ClusterManagerClient{}, err
	}

	return *clusterMgrClient, nil
}

// remove is a helper function that removes an element at the specified index
// from a string array.
func remove(slice []string, s int) []string {
	return append(slice[:s], slice[s+1:]...)
}

func getClientsetFromKubeconfig(kubeconfig []byte) (*kubernetes.Clientset, error) {

	tmpFile, err := ioutil.TempFile("/tmp", "kubeconfig")
	err = ioutil.WriteFile(tmpFile.Name(), kubeconfig, 0640)
	defer os.Remove(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("error building kubeconfig: %s", err.Error())
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", tmpFile.Name())
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("error building kubeconfig: %s", err.Error())
	}

	return clientset, nil
}

// GenerateServiceAccountToken generate a serviceAccountToken for clusterAdmin given a clientset
func generateServiceAccountToken(clientset kubernetes.Interface) (string, error) {

	token := ""
	namespace := "default"
	name := "kontainer-engine"

	// Create new service account, if it does not exist already

	serviceAccount := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}}

	_, _ = clientset.CoreV1().ServiceAccounts(namespace).Create(serviceAccount)

	// Look up service account
	serviceAccount, err := clientset.CoreV1().ServiceAccounts(namespace).Get(serviceAccount.Name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	if len(serviceAccount.Secrets) > 0 {
		secret := serviceAccount.Secrets[0]
		secretObj, err := clientset.CoreV1().Secrets(namespace).Get(secret.Name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("error getting secret: %v", err)
		}
		if byteToken, ok := secretObj.Data["token"]; ok {
			token = string(byteToken)
		}
	}

	// Create new cluster-role-bindings, if it does not exist already
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, APIGroup: "", Name: name, Namespace: namespace}},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: "cluster-admin",
		},
	}

	_, _ = clientset.RbacV1().ClusterRoleBindings().Create(clusterRoleBinding)

	// Look up cluster role binding
	clusterRoleBinding, err = clientset.RbacV1().ClusterRoleBindings().Get(clusterRoleBinding.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error getting cluster role binding: %v", err)
	}

	return token, nil
}
