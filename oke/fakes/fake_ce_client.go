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

package fakes

import (
	"bytes"
	"context"
	"github.com/oracle/oci-go-sdk/common"
	"github.com/oracle/oci-go-sdk/containerengine"
	"io/ioutil"
	"k8s.io/apimachinery/pkg/util/uuid"
	"time"
)

type ContainerEngineClient struct {
	//ContainerEngineClientInterface

	clusters  map[string]containerengine.Cluster
	nodepools map[string]containerengine.NodePool

	workRequests map[string]containerengine.WorkRequest
}

// NewContainerEngineClient returns a client that will respond with the provided objects.
// It shouldn't be used as a replacement for a real client and is mostly useful in simple unit tests.
//func NewContainerEngineClient() (client ContainerEngineClient, err error) {
func NewContainerEngineClient() (client ContainerEngineClient, err error) {
	c := ContainerEngineClient{}
	c.clusters = make(map[string]containerengine.Cluster)
	c.nodepools = make(map[string]containerengine.NodePool)
	c.workRequests = make(map[string]containerengine.WorkRequest)
	return c, nil
}

func (client ContainerEngineClient) CreateCluster(ctx context.Context, request containerengine.CreateClusterRequest) (response containerengine.CreateClusterResponse, err error) {
	cl := containerengine.Cluster{}
	ocid := string(uuid.NewUUID())
	cl.Id = &ocid
	cl.Name = request.Name
	cl.VcnId = request.VcnId
	cl.KubernetesVersion = request.KubernetesVersion
	cl.CompartmentId = request.CompartmentId
	cl.Options = request.Options
	client.clusters[ocid] = cl

	wr := containerengine.WorkRequest{}
	wr.Id = &ocid
	client.workRequests[ocid] = wr

	response = containerengine.CreateClusterResponse{}
	response.OpcWorkRequestId = wr.Id

	return response, nil
}

func (client ContainerEngineClient) CreateKubeconfig(ctx context.Context, request containerengine.CreateKubeconfigRequest) (response containerengine.CreateKubeconfigResponse, err error) {

	kubeconfig := `
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: http://127.1.2.3:6443
  name: ` + *request.ClusterId + `
contexts:
- context:
    cluster: integration
    user: test
  name: default-context
current-context: default-context
users:
- name: test
  user:
    password: test
    username: test
`
	response = containerengine.CreateKubeconfigResponse{Content: ioutil.NopCloser(bytes.NewReader([]byte(kubeconfig)))}
	return response, nil
}

func (client ContainerEngineClient) CreateNodePool(ctx context.Context, request containerengine.CreateNodePoolRequest) (response containerengine.CreateNodePoolResponse, err error) {
	np := containerengine.NodePool{}
	ocid := string(uuid.NewUUID())
	np.Id = &ocid
	np.ClusterId = request.ClusterId
	np.Name = request.Name
	np.CompartmentId = request.CompartmentId
	np.KubernetesVersion = request.KubernetesVersion
	np.SubnetIds = request.SubnetIds
	np.QuantityPerSubnet = request.QuantityPerSubnet
	np.InitialNodeLabels = request.InitialNodeLabels
	client.nodepools[ocid] = np

	wr := containerengine.WorkRequest{}
	wr.Id = &ocid
	client.workRequests[ocid] = wr

	response = containerengine.CreateNodePoolResponse{}
	response.OpcWorkRequestId = wr.Id

	return response, nil
}

func (client ContainerEngineClient) DeleteCluster(ctx context.Context, request containerengine.DeleteClusterRequest) (response containerengine.DeleteClusterResponse, err error) {
	delete(client.clusters, *request.ClusterId)
	wr := containerengine.WorkRequest{}
	wr.Id = request.ClusterId

	response = containerengine.DeleteClusterResponse{}
	response.OpcWorkRequestId = wr.Id

	return response, nil
}

func (client ContainerEngineClient) DeleteNodePool(ctx context.Context, request containerengine.DeleteNodePoolRequest) (response containerengine.DeleteNodePoolResponse, err error) {

	delete(client.nodepools, *request.NodePoolId)
	wr := containerengine.WorkRequest{}
	wr.Id = request.NodePoolId

	response = containerengine.DeleteNodePoolResponse{}
	response.OpcWorkRequestId = wr.Id

	return response, nil
}

func (client ContainerEngineClient) GetCluster(ctx context.Context, request containerengine.GetClusterRequest) (response containerengine.GetClusterResponse, err error) {
	response = containerengine.GetClusterResponse{}
	if cluster, ok := client.clusters[*request.ClusterId]; ok {
		response.Cluster = cluster
		return response, nil
	}
	return response, servicefailure{Message: "Not found", Code: "NotAuthorizedOrNotFound"}
}

func (client ContainerEngineClient) GetClusterOptions(ctx context.Context, request containerengine.GetClusterOptionsRequest) (response containerengine.GetClusterOptionsResponse, err error) {
	response = containerengine.GetClusterOptionsResponse{}
	response.KubernetesVersions = []string{"v1.12.7", "v1.13.5"}
	return response, nil
}

func (client ContainerEngineClient) GetNodePool(ctx context.Context, request containerengine.GetNodePoolRequest) (response containerengine.GetNodePoolResponse, err error) {
	response = containerengine.GetNodePoolResponse{}
	if np, ok := client.nodepools[*request.NodePoolId]; ok {
		response.NodePool = np
		return response, nil
	}
	return response, servicefailure{Message: "Not found", Code: "NotAuthorizedOrNotFound"}
}

func (client ContainerEngineClient) GetWorkRequest(ctx context.Context, request containerengine.GetWorkRequestRequest) (response containerengine.GetWorkRequestResponse, err error) {

	clusterEt := string(containerengine.ListWorkRequestsResourceTypeCluster)

	response = containerengine.GetWorkRequestResponse{}
	if wr, ok := client.workRequests[*request.WorkRequestId]; ok {
		response.Id = wr.Id
		response.TimeFinished = &common.SDKTime{time.Now()}
		response.Resources = []containerengine.WorkRequestResource{{
			ActionType: containerengine.WorkRequestResourceActionTypeCreated,
			EntityType: &clusterEt,
			Identifier: client.clusters[*request.WorkRequestId].Id,
			EntityUri:  nil,
		}}
		return response, nil
	}
	return response, servicefailure{Message: "Not found", Code: "NotAuthorizedOrNotFound"}
}

func (client ContainerEngineClient) ListClusters(ctx context.Context, request containerengine.ListClustersRequest) (response containerengine.ListClustersResponse, err error) {
	response = containerengine.ListClustersResponse{}
	response.Items = []containerengine.ClusterSummary{}
	for _, value := range client.clusters {
		nps := containerengine.ClusterSummary{
			Id:                value.Id,
			CompartmentId:     value.CompartmentId,
			Name:              value.Name,
			KubernetesVersion: value.KubernetesVersion,
		}
		response.Items = append(response.Items, nps)
	}

	return response, nil
}

func (client ContainerEngineClient) ListNodePools(ctx context.Context, request containerengine.ListNodePoolsRequest) (response containerengine.ListNodePoolsResponse, err error) {
	response = containerengine.ListNodePoolsResponse{}
	response.Items = []containerengine.NodePoolSummary{}
	for _, value := range client.nodepools {
		nps := containerengine.NodePoolSummary{
			Id:                value.Id,
			CompartmentId:     value.CompartmentId,
			ClusterId:         value.ClusterId,
			Name:              value.Name,
			KubernetesVersion: value.KubernetesVersion,
			NodeImageId:       value.NodeImageId,
			NodeImageName:     value.NodeImageName,
			NodeShape:         value.NodeShape,
		}
		response.Items = append(response.Items, nps)
	}

	return response, nil
}

func (client ContainerEngineClient) UpdateCluster(ctx context.Context, request containerengine.UpdateClusterRequest) (response containerengine.UpdateClusterResponse, err error) {
	response = containerengine.UpdateClusterResponse{}
	if cl, ok := client.clusters[*request.ClusterId]; ok {
		cl.KubernetesVersion = request.KubernetesVersion
		if request.KubernetesVersion != nil && *request.KubernetesVersion != "" {
			cl.KubernetesVersion = request.KubernetesVersion
		}
		client.clusters[*request.ClusterId] = cl
		wr := containerengine.WorkRequest{}
		wr.Id = cl.Id
		client.workRequests[*cl.Id] = wr
		response.OpcWorkRequestId = wr.Id
		return response, nil
	}
	return response, servicefailure{Message: "Not found", Code: "NotAuthorizedOrNotFound"}
}

func (client ContainerEngineClient) UpdateNodePool(ctx context.Context, request containerengine.UpdateNodePoolRequest) (response containerengine.UpdateNodePoolResponse, err error) {
	response = containerengine.UpdateNodePoolResponse{}
	if np, ok := client.nodepools[*request.NodePoolId]; ok {
		if request.KubernetesVersion != nil && *request.KubernetesVersion != "" {
			np.KubernetesVersion = request.KubernetesVersion
		}
		if request.QuantityPerSubnet != nil {
			np.QuantityPerSubnet = request.QuantityPerSubnet
		}
		client.nodepools[*request.NodePoolId] = np
		wr := containerengine.WorkRequest{}
		wr.Id = np.Id
		client.workRequests[*np.Id] = wr
		response.OpcWorkRequestId = wr.Id
		return response, nil
	}
	return response, servicefailure{Message: "Not found", Code: "NotAuthorizedOrNotFound"}
}

