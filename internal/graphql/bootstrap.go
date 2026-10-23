// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"fmt"
	"net/http"

	"github.com/midnight/midnight/internal/api"
)

// MountConfig bundles the inputs the cmd/midnight-server uses to wire a
// GraphQL gateway alongside the v1alpha REST API. Both handlers are
// returned as http.Handler so the caller can stash them on the
// api.DataStack and re-use the rest of the wire-up unchanged.
type MountConfig struct {
	// Stack is the partially-populated DataStack the REST router is
	// about to consume. Mount mutates Stack to install in-memory
	// defaults for any nil store so the GraphQL gateway and the
	// REST surface share the same stores.
	Stack *api.DataStack

	// DevPlayground enables the /graphiql HTML endpoint. Production
	// deployments leave it false.
	DevPlayground bool
}

// Mount constructs the GraphQL handler and (optionally) the GraphiQL
// playground handler from the supplied configuration. The caller is
// responsible for writing the returned handlers back onto its
// api.DataStack before passing it to api.NewRouterWithDataStack.
func Mount(cfg MountConfig) (graph http.Handler, playground http.Handler, err error) {
	if cfg.Stack == nil {
		return nil, nil, fmt.Errorf("graphql: Mount requires a non-nil DataStack pointer")
	}
	services := api.BuildServicesAPI(cfg.Stack)
	executor, err := NewExecutor(services)
	if err != nil {
		return nil, nil, fmt.Errorf("graphql: build executor: %w", err)
	}
	executor.Deployments = cfg.Stack.DeploymentStore
	executor.DeploymentActions = cfg.Stack.DeploymentActions
	graph = NewHandler(HandlerOptions{
		Executor:               executor,
		BearerToken:            cfg.Stack.APIBearerToken,
		ProductionAuthRequired: cfg.Stack.ProductionAuthRequired,
	})
	if cfg.DevPlayground {
		playground = PlaygroundHandler("/graphql/v2")
	}
	return graph, playground, nil
}
