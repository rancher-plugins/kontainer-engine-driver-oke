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

import "math/rand"

func generateUniqueLabel(prefix string, length int) string {
	bytes := make([]byte, length)
	for i := 0; i < length; i++ {
		bytes[i] = byte(randInt(65, 90))
	}
	return (prefix + string(bytes))[0:length]
}

func randInt(min int, max int) int {
	return min + rand.Intn(max-min)
}
