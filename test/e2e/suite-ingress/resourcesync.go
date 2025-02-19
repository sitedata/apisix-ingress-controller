// Licensed to the Apache Software Foundation (ASF) under one or more
// contributor license agreements.  See the NOTICE file distributed with
// this work for additional information regarding copyright ownership.
// The ASF licenses this file to You under the Apache License, Version 2.0
// (the "License"); you may not use this file except in compliance with
// the License.  You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ingress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/assert"

	"github.com/apache/apisix-ingress-controller/pkg/id"
	"github.com/apache/apisix-ingress-controller/test/e2e/scaffold"
)

var _ = ginkgo.Describe("suite-ingress: apisix resource sync", func() {
	opts := &scaffold.Options{
		Name:                       "default",
		Kubeconfig:                 scaffold.GetKubeconfig(),
		APISIXConfigPath:           "testdata/apisix-gw-config.yaml",
		IngressAPISIXReplicas:      1,
		HTTPBinServicePort:         80,
		APISIXRouteVersion:         "apisix.apache.org/v2beta3",
		ApisixResourceSyncInterval: "60s",
	}
	s := scaffold.NewScaffold(opts)
	ginkgo.JustBeforeEach(func() {
		backendSvc, backendPorts := s.DefaultHTTPBackend()
		// Create ApisixRoute resource
		ar := fmt.Sprintf(`
apiVersion: apisix.apache.org/v2beta3
kind: ApisixRoute
metadata:
 name: httpbin-route
spec:
 http:
 - name: rule1
   match:
     hosts:
     - httpbin.org
     paths:
       - /ip
   backends:
   - serviceName: %s
     servicePort: %d
   authentication:
     enable: true
     type: keyAuth
`, backendSvc, backendPorts[0])
		assert.Nil(ginkgo.GinkgoT(), s.CreateResourceFromString(ar))
		err := s.EnsureNumApisixUpstreamsCreated(1)
		assert.Nil(ginkgo.GinkgoT(), err, "Checking number of upstreams")
		err = s.EnsureNumApisixRoutesCreated(1)
		assert.Nil(ginkgo.GinkgoT(), err, "Checking number of routes")

		// Create Ingress resource
		ing := fmt.Sprintf(`
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  annotations:
    kubernetes.io/ingress.class: apisix
  name: ingress-route
spec:
  rules:
  - host: local.httpbin.org
    http:
      paths:
      - path: /headers
        pathType: Exact
        backend:
          service:
            name: %s
            port:
              number: %d
`, backendSvc, backendPorts[0])
		assert.Nil(ginkgo.GinkgoT(), s.CreateResourceFromString(ing))

		// Create ApisixConsumer resource
		err = s.ApisixConsumerKeyAuthCreated("foo", "foo-key")
		assert.Nil(ginkgo.GinkgoT(), err)
	})

	ginkgo.It("for modified resource sync consistency", func() {
		// crd resource sync interval
		readyTime := time.Now().Add(60 * time.Second)

		routes, _ := s.ListApisixRoutes()
		assert.Len(ginkgo.GinkgoT(), routes, 2)

		consumers, _ := s.ListApisixConsumers()
		assert.Len(ginkgo.GinkgoT(), consumers, 1)

		for _, route := range routes {
			_ = s.CreateApisixRouteByApisixAdmin(id.GenID(route.Name), []byte(`
{
	"methods": ["GET"],
	"uri": "/anything",
	"plugins": {
		"key-auth": {}
	},
	"upstream": {
		"type": "roundrobin",
		"nodes": {
			"httpbin.org": 1
		}
	}
}`))
		}

		for _, consumer := range consumers {
			_ = s.CreateApisixConsumerByApisixAdmin([]byte(fmt.Sprintf(`
{
	"username": "%s",
	"plugins": {
		"key-auth": {
			"key": "auth-one"
		}
	}
}`, consumer.Username)))
		}

		_ = s.NewAPISIXClient().
			GET("/ip").
			WithHeader("Host", "httpbin.org").
			Expect().
			Status(http.StatusNotFound)

		_ = s.NewAPISIXClient().
			GET("/headers").
			WithHeader("Host", "local.httpbin.org").
			Expect().
			Status(http.StatusNotFound)

		waitTime := time.Until(readyTime).Seconds()
		time.Sleep(time.Duration(waitTime) * time.Second)

		_ = s.NewAPISIXClient().
			GET("/ip").
			WithHeader("Host", "httpbin.org").
			WithHeader("apikey", "foo-key").
			Expect().
			Status(http.StatusOK)

		_ = s.NewAPISIXClient().
			GET("/headers").
			WithHeader("Host", "local.httpbin.org").
			Expect().
			Status(http.StatusOK)

		consumers, _ = s.ListApisixConsumers()
		assert.Len(ginkgo.GinkgoT(), consumers, 1)
		data, _ := json.Marshal(consumers[0])
		assert.Contains(ginkgo.GinkgoT(), string(data), "foo-key")
	})

	ginkgo.It("for deleted resource sync consistency", func() {
		// crd resource sync interval
		readyTime := time.Now().Add(60 * time.Second)

		routes, _ := s.ListApisixRoutes()
		assert.Len(ginkgo.GinkgoT(), routes, 2)

		consumers, _ := s.ListApisixConsumers()
		assert.Len(ginkgo.GinkgoT(), consumers, 1)

		for _, route := range routes {
			_ = s.DeleteApisixRouteByApisixAdmin(id.GenID(route.Name))
		}

		for _, consumer := range consumers {
			s.DeleteApisixConsumerByApisixAdmin(consumer.Username)
		}

		_ = s.NewAPISIXClient().
			GET("/ip").
			WithHeader("Host", "httpbin.org").
			Expect().
			Status(http.StatusNotFound)

		_ = s.NewAPISIXClient().
			GET("/headers").
			WithHeader("Host", "local.httpbin.org").
			Expect().
			Status(http.StatusNotFound)

		routes, _ = s.ListApisixRoutes()
		assert.Len(ginkgo.GinkgoT(), routes, 0)
		consumers, _ = s.ListApisixConsumers()
		assert.Len(ginkgo.GinkgoT(), consumers, 0)

		waitTime := time.Until(readyTime).Seconds()
		time.Sleep(time.Duration(waitTime) * time.Second)

		_ = s.NewAPISIXClient().
			GET("/ip").
			WithHeader("Host", "httpbin.org").
			WithHeader("apikey", "foo-key").
			Expect().
			Status(http.StatusOK)

		_ = s.NewAPISIXClient().
			GET("/headers").
			WithHeader("Host", "local.httpbin.org").
			Expect().
			Status(http.StatusOK)

		consumers, _ = s.ListApisixConsumers()
		assert.Len(ginkgo.GinkgoT(), consumers, 1)
		data, _ := json.Marshal(consumers[0])
		assert.Contains(ginkgo.GinkgoT(), string(data), "foo-key")
	})
})
