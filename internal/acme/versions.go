package acme

// RequiredLegoVersion pins the Lego library version Lane 3 ships against.
// It MUST match the version recorded in versions.yaml and the major+minor
// version of the github.com/go-acme/lego/v4 import in go.mod. The version
// is exported so the cmd/midnight binaries can include it in support
// bundles and provenance.
const RequiredLegoVersion = "v4.17.4"
