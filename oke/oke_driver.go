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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/pkg/errors"
	"github.com/rancher/kontainer-engine/drivers/options"
	"github.com/rancher/kontainer-engine/types"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"strings"
	"time"
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

	// Should K8s API endpoint have private IP (i.e. only accessible from VCN, bastion, or authorized SaaS services)
	// Note, Rancher needs to be able to access the K8s API on the private VCN IP for this to be successful e.g. https://10.0.0.2:6443
	// Typically, that means Rancher is running in the same VCN as the specified control plane subnet (see also control-plane-subnet-name)
	PrivateControlPlane bool

	// Should worker nodes have private IPs (i.e. only accessible from an LB on the service subnet)
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

	//The OCID of the KMS key to be used as the master for Kubernetes secret encryption
	KmsKeyID string

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
	// The IP address range of the Kubernetes Pod IPs
	PodCidr string
	// The IP address range of the Kubernetes Service IPs
	ServiceCidr string
	// Optional name of the bastion subnet
	BastionSubnetName string
	// Optional name of the control plane (Kubernetes API endpoint) subnet
	ControlPlaneSubnetName string
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
	// The optional user_data file path to execute on worker nodes
	NodeUserDataPath string
	// The optional user_data contents to execute on worker nodes
	NodeUserDataContents string
	// The number of nodes in each subnet / availability domain
	QuantityPerSubnet int64
	// Optional limit on the number of nodes in the pool. Default 0.
	LimitNodeCount int64
	// The optional custom boot volume size to use for the nodes
	CustomBootVolumeSize int64
	// The optional number of OCPUs for each node (each OCPU is equivalent to one physical core of an Intel Xeon processor)
	FlexOCPUs int64
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
	logrus.Debugf("[oraclecontainerengine] Remove(...) called")
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
		logrus.Infof("[oraclecontainerengine] Deleting node pool %s", nodePoolID)
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

	logrus.Info("[oraclecontainerengine] Deleting OKE cluster")
	err = oke.DeleteCluster(ctx, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not delete cluster")
	}

	if state.SkipVCNDelete {
		return nil
	}

	// TODO we could be deleting a preexisting VCN here
	logrus.Info("[oraclecontainerengine] Deleting VCN")
	err = oke.DeleteVCN(ctx, vcnID)
	if err != nil {
		return errors.Wrap(err, "could not delete virtual cloud network (VCN)")
	}
	return nil
}

func GetState(info *types.ClusterInfo) (State, error) {
	logrus.Debugf("[oraclecontainerengine] GetState(...) called")
	state := State{}
	err := json.Unmarshal([]byte(info.Metadata["state"]), &state)
	return state, err
}

// GetDriverCreateOptions implements driver interface
func (d *OKEDriver) GetDriverCreateOptions(ctx context.Context) (*types.DriverFlags, error) {
	logrus.Debugf("[oraclecontainerengine] GetDriverCreateOptions(...) called")

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
	driverFlag.Options["kms-key-id"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The OCID of the KMS key to be used as the master for Kubernetes secret encryption",
	}
	driverFlag.Options["kubernetes-version"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The Kubernetes version that will be used for your master and worker nodes e.g. v1.11.9, v1.12.7",
	}
	driverFlag.Options["custom-boot-volume-size"] = &types.Flag{
		Type:  types.IntType,
		Usage: "Optional custom boot volume size for the nodes",
	}
	driverFlag.Options["enable-kubernetes-dashboard"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Enable the kubernetes dashboard",
	}
	driverFlag.Options["flex-ocpus"] = &types.Flag{
		Type:  types.IntType,
		Usage: "Optional number of OCPUs for nodes (requires flexible shape) be specified with --node-shape",
	}
	driverFlag.Options["enable-tiller"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Enable the kubernetes dashboard",
	}
	driverFlag.Options["skip-vcn-delete"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Whether to skip deleting VCN",
	}
	driverFlag.Options["node-user-data-path"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The path to custom cloud-init / user_data for the nodes - file contents will be base64 encoded internally if it is not already",
	}
	driverFlag.Options["node-user-data-contents"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The string of custom cloud-init / user_data for the nodes - string will be base64 encoded internally if it is not already",
	}
	driverFlag.Options["node-public-key-path"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The path to the SSH public key to use for the nodes",
	}
	driverFlag.Options["node-public-key-contents"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The contents of the SSH public key to use for the nodes",
	}
	driverFlag.Options["pod-cidr"] = &types.Flag{
		Type:  types.StringType,
		Usage: "A CIDR IP range from which to assign Kubernetes Pod IPs",
		Default: &types.Default{
			DefaultString: podCIDRBlock,
		},
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
	driverFlag.Options["limit-node-count"] = &types.Flag{
		Type:  types.IntType,
		Usage: "Optional limit on the number of nodes in the pool. Default 0.",
		Default: &types.Default{
			DefaultInt: 0,
		},
	}
	driverFlag.Options["service-cidr"] = &types.Flag{
		Type:  types.StringType,
		Usage: "A CIDR IP range from which to assign Kubernetes Service IPs",
		Default: &types.Default{
			DefaultString: servicesCIDRBlock,
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
	driverFlag.Options["vcn-compartment-id"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The OCID of the compartment in which to find existing VCN exists with the specified name",
	}
	driverFlag.Options["vcn-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of an existing virtual network to be used for OKE cluster creation",
	}
	driverFlag.Options["control-plane-subnet-name"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of the existing subnet to use for the Kubernetes API endpoint",
	}
	driverFlag.Options["load-balancer-subnet-name-1"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of the first existing subnet to use for Kubernetes services / LB",
	}
	driverFlag.Options["load-balancer-subnet-name-2"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The name of the second existing subnet to use for Kubernetes services / LB",
	}
	driverFlag.Options["enable-private-control-plane"] = &types.Flag{
		Type:  types.BoolType,
		Usage: "Whether Kubernetes API endpoint is a private IP only accessible from within the VCN",
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
	logrus.Debugf("[oraclecontainerengine] GetDriverUpdateOptions(...) called")

	driverFlag := types.DriverFlags{
		Options: make(map[string]*types.Flag),
	}
	driverFlag.Options["quantity-per-subnet"] = &types.Flag{
		Type:  types.IntType,
		Usage: "The updated number of worker nodes in each subnet / availability-domain to update. 1 (default) means no updates",
	}
	driverFlag.Options["kubernetes-version"] = &types.Flag{
		Type:  types.StringType,
		Usage: "The updated Kubernetes version",
	}
	return &driverFlag, nil
}

// SetDriverOptions implements driver interface
func GetStateFromOpts(driverOptions *types.DriverOptions) (State, error) {
	logrus.Debugf("[oraclecontainerengine] GetStateFromOpts(...) called")

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
	state.SkipVCNDelete = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "skip-vcn-delete", "skipVcnDelete").(bool)
	state.Fingerprint = options.GetValueFromDriverOptions(driverOptions, types.StringType, "fingerprint", "fingerprint").(string)
	state.KmsKeyID = options.GetValueFromDriverOptions(driverOptions, types.StringType, "kms-key-id", "kmsKeyId").(string)

	state.KubernetesVersion = options.GetValueFromDriverOptions(driverOptions, types.StringType, "kubernetes-version", "kubernetesVersion").(string)
	state.Name = options.GetValueFromDriverOptions(driverOptions, types.StringType, "name").(string)
	state.PrivateKeyContents = options.GetValueFromDriverOptions(driverOptions, types.StringType, "private-key-contents", "privateKeyContents").(string)
	state.PrivateKeyPath = options.GetValueFromDriverOptions(driverOptions, types.StringType, "private-key-path", "privateKeyPath").(string)
	state.PrivateKeyPassphrase = options.GetValueFromDriverOptions(driverOptions, types.StringType, "private-key-passphrase", "privateKeyPassphrase").(string)
	state.Region = options.GetValueFromDriverOptions(driverOptions, types.StringType, "region").(string)
	state.TenancyID = options.GetValueFromDriverOptions(driverOptions, types.StringType, "tenancy-id", "tenancyId").(string)
	state.UserOCID = options.GetValueFromDriverOptions(driverOptions, types.StringType, "user-ocid", "userOcid").(string)
	state.WaitNodesActive = options.GetValueFromDriverOptions(driverOptions, types.IntType, "wait-nodes-active", "waitNodesActive").(int64)
	state.PrivateControlPlane = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "enable-private-control-plane", "enablePrivateControlPlane", "enablePrivatControlPlane").(bool)
	state.PrivateNodes = options.GetValueFromDriverOptions(driverOptions, types.BoolType, "enable-private-nodes", "enablePrivateNodes").(bool)
	state.WorkerNodeIngressCidr = options.GetValueFromDriverOptions(driverOptions, types.StringType, "worker-node-ingress-cidr", "WorkerNodeIngressCidr", "workerNodeIngressCidr").(string)

	state.NodePool = NodeConfiguration{
		FlexOCPUs:                options.GetValueFromDriverOptions(driverOptions, types.IntType, "flex-ocpus", "flexOcpus").(int64),
		CustomBootVolumeSize:     options.GetValueFromDriverOptions(driverOptions, types.IntType, "custom-boot-volume-size", "customBootVolumeSize").(int64),
		NodeImageName:            options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-image", "nodeImage").(string),
		NodeShape:                options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-shape", "nodeShape").(string),
		NodePublicSSHKeyPath:     options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-public-key-path", "nodePublicKeyPath").(string),
		NodePublicSSHKeyContents: options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-public-key-contents", "nodePublicKeyContents").(string),
		NodeUserDataPath:         options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-user-data-path", "nodeUserDataPath").(string),
		NodeUserDataContents:     options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-user-data-contents", "nodeUserDataContents").(string),
		QuantityPerSubnet:        options.GetValueFromDriverOptions(driverOptions, types.IntType, "quantity-per-subnet", "quantityPerSubnet").(int64),
		LimitNodeCount:           options.GetValueFromDriverOptions(driverOptions, types.IntType, "limit-node-count", "limitNodeCount").(int64),
	}

	state.Network = NetworkConfiguration{
		VcnCompartmentID:               options.GetValueFromDriverOptions(driverOptions, types.StringType, "vcn-compartment-id", "vcnCompartmentId").(string),
		VCNName:                        options.GetValueFromDriverOptions(driverOptions, types.StringType, "vcn-name", "vcnName").(string),
		BastionSubnetName:              options.GetValueFromDriverOptions(driverOptions, types.StringType, "bastion-subnet-name", "bastionSubnetName").(string),
		ControlPlaneSubnetName:         options.GetValueFromDriverOptions(driverOptions, types.StringType, "control-plane-subnet-name", "controlPlaneSubnetName").(string),
		ServiceLBSubnet1Name:           options.GetValueFromDriverOptions(driverOptions, types.StringType, "load-balancer-subnet-name-1", "loadBalancerSubnetName1").(string),
		ServiceLBSubnet2Name:           options.GetValueFromDriverOptions(driverOptions, types.StringType, "load-balancer-subnet-name-2", "loadBalancerSubnetName2").(string),
		QuantityOfSubnets:              options.GetValueFromDriverOptions(driverOptions, types.IntType, "quantity-of-node-subnets", "quantityOfNodeSubnets").(int64),
		PodCidr:                        options.GetValueFromDriverOptions(driverOptions, types.StringType, "pod-cidr", "podCidr").(string),
		ServiceCidr:                    options.GetValueFromDriverOptions(driverOptions, types.StringType, "service-cidr", "serviceCidr").(string),
		NodePoolSubnetName:             options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-pool-subnet-name", "nodePoolSubnetName").(string),
		NodePoolSubnetSecurityListName: options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-pool-subnet-security-list-name", "nodePoolSubnetSecurityListName").(string),
		NodePoolSubnetDnsDomainName:    options.GetValueFromDriverOptions(driverOptions, types.StringType, "node-pool-dns-domain-list-name", "nodePoolSubnetDnsDomainName").(string),
		ServiceSubnetSecurityListName:  options.GetValueFromDriverOptions(driverOptions, types.StringType, "service-subnet-security-list-name", "serviceSubnetSecurityListName").(string),
		ServiceSubnetDnsDomainName:     options.GetValueFromDriverOptions(driverOptions, types.StringType, "service-subnet-dns-domain-name", "serviceSubnetDnsDomainName").(string),
	}

	if state.Network.VCNName != "" {
		state.SkipVCNDelete = true
	}

	if state.Network.NodePoolSubnetName == "" {
		state.Network.NodePoolSubnetName = nodeSubnetName
	}

	if state.Network.ControlPlaneSubnetName == "" {
		state.Network.ControlPlaneSubnetName = controlPlaneSubnetName
	}

	if state.Network.BastionSubnetName == "" {
		state.Network.BastionSubnetName = bastionSubnetName
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

	if state.NodePool.NodeUserDataContents != "" {
		userDataBytes := []byte(state.NodePool.NodeUserDataContents)
		if !isBase64Encoded(userDataBytes) {
			// String was not base64 encoded.
			state.NodePool.NodeUserDataContents = base64.StdEncoding.EncodeToString(userDataBytes)
		}
	} else if state.NodePool.NodeUserDataContents == "" && state.NodePool.NodeUserDataPath != "" {
		userDataBytes, err := ioutil.ReadFile(state.NodePool.NodeUserDataPath)
		if err == nil {
			if !isBase64Encoded(userDataBytes) {
				// Files was not base64 encoded.
				state.NodePool.NodeUserDataContents = base64.StdEncoding.EncodeToString(userDataBytes)
			} else {
				state.NodePool.NodeUserDataContents = string(userDataBytes)
			}
		}
	}

	return state, state.validate()
}

func (s *State) validate() error {
	logrus.Debugf("[oraclecontainerengine] validate(...) called")
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
	} else if s.NodePool.CustomBootVolumeSize != 0 && (s.NodePool.CustomBootVolumeSize < 50 || s.NodePool.CustomBootVolumeSize > 16384) {
		return fmt.Errorf(`"custom-boot-size", if set, must be larger than 50 and smaller than 32768 (GB)"`)
	} else if s.NodePool.FlexOCPUs > 0 && !strings.Contains(strings.ToLower(s.NodePool.NodeShape), "flex") {
		return fmt.Errorf(`"flex-ocpus", requires nodes to use a flexible shape with --node-shape"`)
	} else if s.NodePool.FlexOCPUs != 0 && (s.NodePool.FlexOCPUs > 64) {
		return fmt.Errorf(`"flex-ocpus", if set, must not be larger than 64"`)
	}

	return nil
}

// Create implements driver interface
func (d *OKEDriver) Create(ctx context.Context, opts *types.DriverOptions, _ *types.ClusterInfo) (*types.ClusterInfo, error) {
	logrus.Debugf("[oraclecontainerengine] Create(...) called")

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
		logrus.Debugf("[oraclecontainerengine] error storing state %v", err)
		return clusterInfo, err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return clusterInfo, err
	}

	var vcnID string
	var controlPlaneSubnetID string
	var serviceSubnetIDs, nodeSubnetIds []string

	// Check if we should create a new VCN or make use of an existing VCN and subnets.
	if state.Network.VCNName == "" {
		// Create a new Virtual Cloud Network with the default number of subnets
		state.Network.VCNName = defaultVCNNamePrefix + state.Name

		if state.Network.ServiceLBSubnet1Name == "" {
			state.Network.ServiceLBSubnet1Name = defaultServiceSubnetPrefix + state.Name
		}
		if state.Network.NodePoolSubnetName == "" {
			state.Network.NodePoolSubnetName = nodeSubnetName
		}
		state.Network.QuantityOfSubnets = 1

		// the case where we are creating the vcn, and the cluster did not finish successfully,
		// we want to perform a manual recreate
		vcnAlreadyCreated, err := oke.GetVcnByName(ctx, state.CompartmentID, state.Network.VCNName)
		if err == nil && len(vcnAlreadyCreated.String()) > 0 {
			logrus.Debugf("[oraclecontainerengine] Info: recreating vcn %v in compartment %s", state.Network.VCNName, state.CompartmentID)
			// a previous attempt failed, so let's delete this one and create a new one below
			oke.DeleteVCN(ctx, *vcnAlreadyCreated.Id)
		}

		logrus.Infof("[oraclecontainerengine] Creating a new VCN and required network resources for OKE cluster %s", state.Name)
		vcnID, controlPlaneSubnetID, serviceSubnetIDs, nodeSubnetIds, err = oke.CreateVCNAndNetworkResources(&state)

		if err != nil {
			logrus.Debugf("[oraclecontainerengine] error creating the VCN and/or the required network resources %v", err)
			return clusterInfo, err
		}

	} else {
		// Use an existing VCN and subnets. Besides the VCN and subnets, we are
		// assuming that the internet gateway, route table, security lists are present and configured correctly.
		logrus.Debugf("[oraclecontainerengine] using an existing VCN %s", state.Network.VCNName)
		vcnID, err = oke.GetVcnIDByName(ctx, state.Network.VcnCompartmentID, state.Network.VCNName)
		if err != nil {
			logrus.Debugf("[oraclecontainerengine] error looking up the Id of existing VCN %s %v", state.Network.VCNName, err)
			return clusterInfo, err
		}

		// Will pick up a subnet if specified or a preexisting subnet that uses the default subnet name
		controlPlaneSubnetID, err = oke.GetSubnetIDByName(ctx, state.Network.VcnCompartmentID, vcnID, state.Network.ControlPlaneSubnetName)
		if err != nil {
			// For backwards compatibility, we can create a cluster without specifying the control-plane subnet.
			logrus.Infof("[oraclecontainerengine] warning: unable to look up the Id the Kubernetes control-plane subnet %s %v", state.Network.ControlPlaneSubnetName, err)
		}

		serviceSubnet1Id, err := oke.GetSubnetIDByName(ctx, state.Network.VcnCompartmentID, vcnID, state.Network.ServiceLBSubnet1Name)
		if err != nil {
			logrus.Debugf("[oraclecontainerengine] error looking up the Id of a Kubernetes service Subnet %s %v", state.Network.ServiceLBSubnet1Name, err)
			return clusterInfo, err
		}
		serviceSubnetIDs = append(serviceSubnetIDs, serviceSubnet1Id)

		if state.Network.ServiceLBSubnet2Name != "" {
			serviceSubnet2Id, err := oke.GetSubnetIDByName(ctx, state.Network.VcnCompartmentID, vcnID, state.Network.ServiceLBSubnet2Name)
			if err != nil {
				logrus.Debugf("[oraclecontainerengine] error looking up the Id of a Kubernetes service Subnet %s %v", state.Network.ServiceLBSubnet2Name, err)
				return clusterInfo, err
			}
			serviceSubnetIDs = append(serviceSubnetIDs, serviceSubnet2Id)
		}

		nodeSubnetId, err := oke.GetSubnetIDByName(ctx, state.Network.VcnCompartmentID, vcnID, state.Network.NodePoolSubnetName)
		if err != nil {
			// If a custom node pool subnet net was explicitly passed in, error out if it is not found.
			if state.Network.NodePoolSubnetName != nodeSubnetName {
				logrus.Debugf("[oraclecontainerengine] error looking up the Id of a Kubernetes node Subnet %s %v", state.Network.NodePoolSubnetName, err)
				return clusterInfo, err
			}
			// Node pool subnet name was not passed in, attempt to deduce it
			nodeSubnetIds, _ = oke.ListSubnetIdsInVcn(ctx, state.Network.VcnCompartmentID, vcnID)
			for _, knownSubnet := range append(serviceSubnetIDs, controlPlaneSubnetID) {
				for i, nextSubnetId := range nodeSubnetIds {
					// Remove the known service subnet, control-plane, and/or bastion subnets from the list
					if nextSubnetId == knownSubnet {
						nodeSubnetIds = remove(nodeSubnetIds, i)
						break
					}
				}
			}
			// We can only be reasonably assured if we've found a single match.
			if len(nodeSubnetIds) != 1 {
				// User needs to disambiguate.
				return clusterInfo, fmt.Errorf("cannot determine node subnet ID, please set --node-pool-subnet-name")
			}
		} else {
			nodeSubnetIds = append(nodeSubnetIds, nodeSubnetId)
		}

		// For existing VCNs, ensure private-control-plane and private-nodes settings are in sync with subnet access
		if len(controlPlaneSubnetID) > 0 {
			controlPlaneSubnet, err := oke.GetSubnetById(ctx, controlPlaneSubnetID)
			if err != nil {
				logrus.Debugf("[oraclecontainerengine] error fetching Kubernetes control-plane subnet subnet %v", err)
				return clusterInfo, err
			}
			if *controlPlaneSubnet.ProhibitPublicIpOnVnic != state.PrivateControlPlane {
				state.PrivateControlPlane = *controlPlaneSubnet.ProhibitPublicIpOnVnic
			}
		}
		for _, nextNodeSubnetId := range nodeSubnetIds {
			nextNodeSubnet, err := oke.GetSubnetById(ctx, nextNodeSubnetId)
			if err != nil {
				logrus.Debugf("[oraclecontainerengine] error fetching node subnet %v", err)
				return clusterInfo, err
			}
			if *nextNodeSubnet.ProhibitPublicIpOnVnic != state.PrivateNodes {
				state.PrivateNodes = *nextNodeSubnet.ProhibitPublicIpOnVnic
			}
		}
	}

	state.Network.QuantityOfSubnets = int64(len(nodeSubnetIds))

	clusterID, err := oke.GetClusterByName(ctx, state.CompartmentID, state.Name)
	if err == nil && len(clusterID) > 0 {
		logrus.Debugf("[oraclecontainerengine] warning: an existing cluster with name %s already exists in compartment %s", state.Name, state.CompartmentID)
		logrus.Debugf("[oraclecontainerengine] removing cluster %s  as part of recreate attempt", state.ClusterID)
		oke.DeleteCluster(ctx, clusterID)
	}

	logrus.Infof("[oraclecontainerengine] Creating OKE cluster %s", state.Name)
	err = oke.CreateCluster(ctx, &state, vcnID, controlPlaneSubnetID, serviceSubnetIDs, nodeSubnetIds)
	if err != nil {
		logrus.Debugf("[oraclecontainerengine] error creating the OKE cluster %v", err)

		return clusterInfo, err
	}
	err = storeState(clusterInfo, state)
	if err != nil {
		logrus.Debugf("[oraclecontainerengine] error storing state %v", err)
		return clusterInfo, err
	}

	logrus.Infof("[oraclecontainerengine] Creating node pool for cluster %s", state.Name)
	err = oke.CreateNodePool(ctx, &state, vcnID, serviceSubnetIDs, nodeSubnetIds)
	if err != nil {
		logrus.Debugf("[oraclecontainerengine] error creating the node pool %v", err)
		return clusterInfo, err
	}

	err = storeState(clusterInfo, state)
	if err != nil {
		logrus.Debugf("[oraclecontainerengine] error storing state %v", err)
		return clusterInfo, err
	}

	return clusterInfo, nil
}

// Update implements driver interface
func (d *OKEDriver) Update(ctx context.Context, info *types.ClusterInfo, opts *types.DriverOptions) (*types.ClusterInfo, error) {
	logrus.Debugf("[oraclecontainerengine] Update(...) called")

	state, err := GetState(info)
	if err != nil {
		return nil, err
	}
	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return nil, errors.Wrap(err, "could not get Oracle Container Engine client")
	}

	newState, err := GetStateFromOpts(opts)
	if err != nil {
		return nil, err
	}

	if limitN(int(newState.NodePool.QuantityPerSubnet), int(newState.NodePool.LimitNodeCount)) !=
		limitN(int(state.NodePool.QuantityPerSubnet), int(state.NodePool.LimitNodeCount)) {
		logrus.Infof("[oraclecontainerengine] Updating quantity of nodes in the node-pool to %d", limitN(int(newState.NodePool.QuantityPerSubnet*int64(oke.numADs(ctx, state.CompartmentID))), int(newState.NodePool.LimitNodeCount)))
		err = d.SetClusterSize(ctx, info, &types.NodeCount{Count: int64(limitN(int(newState.NodePool.QuantityPerSubnet*int64(oke.numADs(ctx, state.CompartmentID))), int(newState.NodePool.LimitNodeCount)))})
		if err != nil {
			return nil, err
		}
		state.NodePool.QuantityPerSubnet = newState.NodePool.QuantityPerSubnet
	}
	if newState.KubernetesVersion != state.KubernetesVersion {
		logrus.Infof("[oraclecontainerengine] Updating Kubernetes version to %s", newState.KubernetesVersion)
		err = d.SetVersion(ctx, info, &types.KubernetesVersion{Version: string(newState.KubernetesVersion)})
		if err != nil {
			return nil, err
		}
		state.KubernetesVersion = newState.KubernetesVersion
	}

	logrus.Info("[oraclecontainerengine] Cluster updates may continue asynchronously")
	return info, storeState(info, state)
}

func (d *OKEDriver) PostCheck(ctx context.Context, info *types.ClusterInfo) (*types.ClusterInfo, error) {
	logrus.Debugf("[oraclecontainerengine] PostCheck(...) called")
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

	if cluster.Endpoints.Kubernetes != nil && len(*cluster.Endpoints.Kubernetes) > 0 {
		info.Endpoint = *cluster.Endpoints.Kubernetes
	} else {
		if state.PrivateControlPlane {
			logrus.Info("[oraclecontainerengine] warning: access to the Kubernetes API endpoint is limited to the VCN, a bastion host, or authorized SaaS services")
			info.Endpoint = *cluster.Endpoints.PrivateEndpoint
		} else {
			info.Endpoint = *cluster.Endpoints.PublicEndpoint
		}
	}
	info.Version = *cluster.KubernetesVersion
	info.Username = ""
	info.Password = ""
	info.ClientCertificate = ""
	info.ClientKey = ""
	info.NodeCount = state.NodePool.QuantityPerSubnet * int64(oke.numADs(ctx, state.CompartmentID))
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
		if state.PrivateControlPlane {
			// Avoid unnecessary cluster creation when state.PrivateControlPlane == true and inaccessible by Rancher.
			// Results in cluster stuck in "Waiting for API to be available" (rather than the cluster being re-created over and over).
			// Alternatively, we could set a more useful error message about the API server not being reachable.
			return info, nil
		}
		return nil, errors.Wrap(err, "could not generate service account token from OKE kubeconfig")
	}

	return info, nil
}

func (d *OKEDriver) GetClusterSize(ctx context.Context, info *types.ClusterInfo) (*types.NodeCount, error) {
	logrus.Debugf("[oraclecontainerengine] GetClusterSize(...) called")
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
	logrus.Debugf("[oraclecontainerengine] GetVersion(...) called")
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
	logrus.Debugf("[oraclecontainerengine] SetClusterSize(...) called")
	state, err := GetState(info)
	if err != nil {
		return err
	}

	oke, err := constructClusterManagerClient(ctx, state)
	if err != nil {
		return errors.Wrap(err, "could not get Oracle Container Engine client")
	}

	logrus.Infof("[oraclecontainerengine] Updating the number of nodes in the node-pool to %d", count.Count)
	nodePoolIds, err := oke.ListNodepoolIdsInCluster(ctx, state.CompartmentID, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not retrieve node pool id")
	}
	if len(nodePoolIds) <= 0 {
		return fmt.Errorf("could not retrieve node pool id")
	}

	err = oke.ScaleNodePool(ctx, nodePoolIds[0], int(count.Count), state.CompartmentID)
	if err != nil {
		return errors.Wrap(err, "could not adjust the number of nodes in the pool")
	}

	// scale is currently asynchronous
	logrus.Info("[oraclecontainerengine] Cluster size updated successfully")
	return nil
}

func (d *OKEDriver) SetVersion(ctx context.Context, info *types.ClusterInfo, version *types.KubernetesVersion) error {
	logrus.Debugf("[oraclecontainerengine] SetVersion(...) called")

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
		logrus.Debugf("[oraclecontainerengine] warning: could not update the version of Kubernetes master(s)")
	}

	nodePoolIds, err := oke.ListNodepoolIdsInCluster(ctx, state.CompartmentID, state.ClusterID)
	if err != nil {
		return errors.Wrap(err, "could not retrieve node pool id")
	}

	var npErr error
	for _, nodePoolID := range nodePoolIds {
		nodePool, err := oke.GetNodePoolByID(ctx, nodePoolID)
		if err != nil {
			logrus.Debugf("[oraclecontainerengine] could not retrieve node pool")
			continue
		}
		logrus.Infof("[oraclecontainerengine] Updating the version of Kubernetes to %s", version.Version)
		nextNpErr := oke.UpdateNodepoolKubernetesVersion(ctx, *nodePool.Id, version.Version)
		if nextNpErr != nil {
			npErr = nextNpErr
			logrus.Debugf("[oraclecontainerengine] warning: could not update the version of Kubernetes master(s)")
		}
	}

	if mErr != nil {
		return errors.Wrap(mErr, "could not update the version of Kubernetes master(s)")
	} else if npErr != nil {
		return errors.Wrap(npErr, "could not update the Kubernetes version of one the node pool")
	}

	// update version is currently asynchronous
	logrus.Info("[oraclecontainerengine] Cluster version (masters and node pools) updated successfully")
	return nil
}

func (d *OKEDriver) GetCapabilities(ctx context.Context) (*types.Capabilities, error) {
	logrus.Debugf("[oraclecontainerengine] GetCapabilities(...) called")
	return &d.driverCapabilities, nil
}

func (d *OKEDriver) ETCDSave(ctx context.Context, clusterInfo *types.ClusterInfo, opts *types.DriverOptions, snapshotName string) error {
	logrus.Debugf("[oraclecontainerengine] ETCDSave(...) called")
	return fmt.Errorf("ETCD backup operations are not implemented")
}

func (d *OKEDriver) ETCDRestore(ctx context.Context, clusterInfo *types.ClusterInfo, opts *types.DriverOptions, snapshotName string) (*types.ClusterInfo, error) {
	logrus.Debugf("[oraclecontainerengine] ETCDRestore(...) called")
	return nil, fmt.Errorf("ETCD backup operations are not implemented")
}

func (d *OKEDriver) ETCDRemoveSnapshot(ctx context.Context, clusterInfo *types.ClusterInfo, opts *types.DriverOptions, snapshotName string) error {
	logrus.Debugf("[oraclecontainerengine] ETCDRemoveSnapshot(...) called")
	return fmt.Errorf("ETCD backup operations are not implemented")
}

func (d *OKEDriver) GetK8SCapabilities(ctx context.Context, options *types.DriverOptions) (*types.K8SCapabilities, error) {
	logrus.Debugf("[oraclecontainerengine] GetK8SCapabilities(...) called")

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
	logrus.Debugf("[oraclecontainerengine] RemoveLegacyServiceAccount(...) called")
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

	serverVersion, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return "", err
	}
	logrus.Debugf("[oraclecontainerengine] Kubernetes server version: %s", serverVersion)

	serviceAccount := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name}}

	// Create new service account, if it does not exist already
	_, err = clientset.CoreV1().ServiceAccounts(namespace).Create(context.Background(), serviceAccount, metav1.CreateOptions{})
	if err != nil {
		if !apierror.IsAlreadyExists(err) {
			return "", err
		}
	}

	serviceAccount, err = clientset.CoreV1().ServiceAccounts(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	// Template for an authentication token secret bound to the service account
	secretTemplate := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceAccount.Name + "-token",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "ServiceAccount",
					Name:       serviceAccount.Name,
					UID:        serviceAccount.UID,
				},
			},
			Annotations: map[string]string{
				v1.ServiceAccountNameKey: serviceAccount.Name,
			},
		},
		Type: v1.SecretTypeServiceAccountToken,
	}

	_, err = clientset.CoreV1().Secrets(namespace).Create(context.Background(), secretTemplate, metav1.CreateOptions{})
	if err != nil {
		if !apierror.IsAlreadyExists(err) {
			return "", err
		}
	}
	// wait a few seconds for authentication token to populate
	time.Sleep(10 * time.Second)

	secretObj, err := clientset.CoreV1().Secrets(namespace).Get(context.Background(), serviceAccount.Name+"-token", metav1.GetOptions{})
	if err != nil {
		return "", err
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

	_, err = clientset.RbacV1().ClusterRoleBindings().Create(context.Background(), clusterRoleBinding, metav1.CreateOptions{})
	if err != nil {
		if !apierror.IsAlreadyExists(err) {
			return "", err
		}
	}

	// Look up cluster role binding
	_, err = clientset.RbacV1().ClusterRoleBindings().Get(context.Background(), clusterRoleBinding.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("error getting cluster role binding: %v", err)
	}

	// get the bearer token from the token-secret
	if byteToken, ok := secretObj.Data[v1.ServiceAccountTokenKey]; ok {
		token = string(byteToken)
		return token, nil
	}

	return "", fmt.Errorf("error getting authentication token from secret: %s", secretObj.Name)
}

func isBase64Encoded(data []byte) bool {
	_, err := base64.StdEncoding.DecodeString(string(data))
	return err == nil
}
