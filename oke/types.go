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

import (
	"context"

	ocice "github.com/oracle/oci-go-sdk/containerengine"
	ocicore "github.com/oracle/oci-go-sdk/core"
	ociid "github.com/oracle/oci-go-sdk/identity"
)

// ContainerEngineClientInterface defines an interface for the oci ContainerEngine client to be implemented by real and fake clients
type ContainerEngineClientInterface interface {
	CreateCluster(ctx context.Context, request ocice.CreateClusterRequest) (response ocice.CreateClusterResponse, err error)
	CreateKubeconfig(ctx context.Context, request ocice.CreateKubeconfigRequest) (response ocice.CreateKubeconfigResponse, err error)
	CreateNodePool(ctx context.Context, request ocice.CreateNodePoolRequest) (response ocice.CreateNodePoolResponse, err error)
	DeleteCluster(ctx context.Context, request ocice.DeleteClusterRequest) (response ocice.DeleteClusterResponse, err error)
	DeleteNodePool(ctx context.Context, request ocice.DeleteNodePoolRequest) (response ocice.DeleteNodePoolResponse, err error)
	GetCluster(ctx context.Context, request ocice.GetClusterRequest) (response ocice.GetClusterResponse, err error)
	GetClusterOptions(ctx context.Context, request ocice.GetClusterOptionsRequest) (response ocice.GetClusterOptionsResponse, err error)
	GetNodePool(ctx context.Context, request ocice.GetNodePoolRequest) (response ocice.GetNodePoolResponse, err error)
	GetWorkRequest(ctx context.Context, request ocice.GetWorkRequestRequest) (response ocice.GetWorkRequestResponse, err error)
	ListClusters(ctx context.Context, request ocice.ListClustersRequest) (response ocice.ListClustersResponse, err error)
	ListNodePools(ctx context.Context, request ocice.ListNodePoolsRequest) (response ocice.ListNodePoolsResponse, err error)
	UpdateCluster(ctx context.Context, request ocice.UpdateClusterRequest) (response ocice.UpdateClusterResponse, err error)
	UpdateNodePool(ctx context.Context, request ocice.UpdateNodePoolRequest) (response ocice.UpdateNodePoolResponse, err error)
}

// IdentityClientInterface defines an interface for the oci identity client to be implemented by real and fake clients
type IdentityClientInterface interface {
	GetCompartment(ctx context.Context, request ociid.GetCompartmentRequest) (response ociid.GetCompartmentResponse, err error)
	CreateCompartment(ctx context.Context, request ociid.CreateCompartmentRequest) (response ociid.CreateCompartmentResponse, err error)
	CreatePolicy(ctx context.Context, request ociid.CreatePolicyRequest) (response ociid.CreatePolicyResponse, err error)
	DeleteCompartment(ctx context.Context, request ociid.DeleteCompartmentRequest) (response ociid.DeleteCompartmentResponse, err error)
	DeletePolicy(ctx context.Context, request ociid.DeletePolicyRequest) (response ociid.DeletePolicyResponse, err error)
	GetPolicy(ctx context.Context, request ociid.GetPolicyRequest) (response ociid.GetPolicyResponse, err error)
	ListAvailabilityDomains(ctx context.Context, request ociid.ListAvailabilityDomainsRequest) (response ociid.ListAvailabilityDomainsResponse, err error)
	ListCompartments(ctx context.Context, request ociid.ListCompartmentsRequest) (response ociid.ListCompartmentsResponse, err error)
	UpdatePolicy(ctx context.Context, request ociid.UpdatePolicyRequest) (response ociid.UpdatePolicyResponse, err error)
}

// VcnClientInterface defines an interface for the oci vcn client to be implemented by real and fake clients
type VcnClientInterface interface {
	CreateDhcpOptions(ctx context.Context, request ocicore.CreateDhcpOptionsRequest) (response ocicore.CreateDhcpOptionsResponse, err error)
	CreateInternetGateway(ctx context.Context, request ocicore.CreateInternetGatewayRequest) (response ocicore.CreateInternetGatewayResponse, err error)
	CreateNatGateway(ctx context.Context, request ocicore.CreateNatGatewayRequest) (response ocicore.CreateNatGatewayResponse, err error)
	CreateRouteTable(ctx context.Context, request ocicore.CreateRouteTableRequest) (response ocicore.CreateRouteTableResponse, err error)
	CreateSecurityList(ctx context.Context, request ocicore.CreateSecurityListRequest) (response ocicore.CreateSecurityListResponse, err error)
	CreateSubnet(ctx context.Context, request ocicore.CreateSubnetRequest) (response ocicore.CreateSubnetResponse, err error)
	CreateVcn(ctx context.Context, request ocicore.CreateVcnRequest) (response ocicore.CreateVcnResponse, err error)
	DeleteDhcpOptions(ctx context.Context, request ocicore.DeleteDhcpOptionsRequest) (response ocicore.DeleteDhcpOptionsResponse, err error)
	DeleteInternetGateway(ctx context.Context, request ocicore.DeleteInternetGatewayRequest) (response ocicore.DeleteInternetGatewayResponse, err error)
	DeleteNatGateway(ctx context.Context, request ocicore.DeleteNatGatewayRequest) (response ocicore.DeleteNatGatewayResponse, err error)
	DeleteRouteTable(ctx context.Context, request ocicore.DeleteRouteTableRequest) (response ocicore.DeleteRouteTableResponse, err error)
	DeleteSecurityList(ctx context.Context, request ocicore.DeleteSecurityListRequest) (response ocicore.DeleteSecurityListResponse, err error)
	DeleteSubnet(ctx context.Context, request ocicore.DeleteSubnetRequest) (response ocicore.DeleteSubnetResponse, err error)
	DeleteVcn(ctx context.Context, request ocicore.DeleteVcnRequest) (response ocicore.DeleteVcnResponse, err error)
	GetDhcpOptions(ctx context.Context, request ocicore.GetDhcpOptionsRequest) (response ocicore.GetDhcpOptionsResponse, err error)
	GetInternetGateway(ctx context.Context, request ocicore.GetInternetGatewayRequest) (response ocicore.GetInternetGatewayResponse, err error)
	GetRouteTable(ctx context.Context, request ocicore.GetRouteTableRequest) (response ocicore.GetRouteTableResponse, err error)
	GetSecurityList(ctx context.Context, request ocicore.GetSecurityListRequest) (response ocicore.GetSecurityListResponse, err error)
	GetSubnet(ctx context.Context, request ocicore.GetSubnetRequest) (response ocicore.GetSubnetResponse, err error)
	GetVcn(ctx context.Context, request ocicore.GetVcnRequest) (response ocicore.GetVcnResponse, err error)
	GetVnic(ctx context.Context, request ocicore.GetVnicRequest) (response ocicore.GetVnicResponse, err error)
	ListInternetGateways(ctx context.Context, request ocicore.ListInternetGatewaysRequest) (response ocicore.ListInternetGatewaysResponse, err error)
	ListNatGateways(ctx context.Context, request ocicore.ListNatGatewaysRequest) (response ocicore.ListNatGatewaysResponse, err error)
	ListRouteTables(ctx context.Context, request ocicore.ListRouteTablesRequest) (response ocicore.ListRouteTablesResponse, err error)
	ListSecurityLists(ctx context.Context, request ocicore.ListSecurityListsRequest) (response ocicore.ListSecurityListsResponse, err error)
	ListSubnets(ctx context.Context, request ocicore.ListSubnetsRequest) (response ocicore.ListSubnetsResponse, err error)
	ListVcns(ctx context.Context, request ocicore.ListVcnsRequest) (response ocicore.ListVcnsResponse, err error)
	UpdateDhcpOptions(ctx context.Context, request ocicore.UpdateDhcpOptionsRequest) (response ocicore.UpdateDhcpOptionsResponse, err error)
	UpdateInternetGateway(ctx context.Context, request ocicore.UpdateInternetGatewayRequest) (response ocicore.UpdateInternetGatewayResponse, err error)
	UpdateRouteTable(ctx context.Context, request ocicore.UpdateRouteTableRequest) (response ocicore.UpdateRouteTableResponse, err error)
	UpdateSecurityList(ctx context.Context, request ocicore.UpdateSecurityListRequest) (response ocicore.UpdateSecurityListResponse, err error)
	UpdateSubnet(ctx context.Context, request ocicore.UpdateSubnetRequest) (response ocicore.UpdateSubnetResponse, err error)
	UpdateVcn(ctx context.Context, request ocicore.UpdateVcnRequest) (response ocicore.UpdateVcnResponse, err error)
}
