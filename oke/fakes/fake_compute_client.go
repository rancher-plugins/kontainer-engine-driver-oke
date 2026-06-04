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
	"context"

	ocicore "github.com/oracle/oci-go-sdk/v65/core"
)

type ComputeClient struct{}

func NewComputeClient() (client *ComputeClient, err error) {
	return &ComputeClient{}, nil
}

func (client *ComputeClient) ListShapes(ctx context.Context, request ocicore.ListShapesRequest) (response ocicore.ListShapesResponse, err error) {
	shapeName := "VM.Standard2.1"
	response = ocicore.ListShapesResponse{}
	if request.AvailabilityDomain != nil && *request.AvailabilityDomain != "AD-1" {
		return response, nil
	}
	response.Items = []ocicore.Shape{{
		Shape: &shapeName,
	}}
	return response, nil
}
