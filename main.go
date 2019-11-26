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

package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/rancher-plugins/kontainer-engine-driver-oke/oke"
	"github.com/rancher/kontainer-engine/types"
	"github.com/sirupsen/logrus"
)

var wg = &sync.WaitGroup{}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "" {
		panic(errors.New("no port provided"))
	}

	port, err := strconv.Atoi(os.Args[1])
	if err != nil {
		panic(fmt.Errorf("argument not parsable as int: %v", err))
	}

	addr := make(chan string)
	go types.NewServer(&oke.OKEDriver{}, addr).ServeOrDie(fmt.Sprintf("127.0.0.1:%v", port))

	logrus.Debugf("oke driver up and running on at %v", <-addr)

	wg.Add(1)
	wg.Wait() // wait forever, we only exit if killed by parent process
}
