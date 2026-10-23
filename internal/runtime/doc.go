// Package runtime owns server-side runtime adapter contracts. It defines
// the abstract RuntimeAdapter boundary required by ADR-015 along with the
// DTOs that flow across it. Concrete adapters live in subpackages
// (`local` for the in-memory Phase 1 adapter; future Docker/containerd/k8s
// adapters land alongside it).
package runtime
