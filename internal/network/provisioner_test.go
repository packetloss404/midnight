package network

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

func TestProjectNetworkNameIsDeterministic(t *testing.T) {
	cases := []struct {
		projectID     string
		environmentID string
		want          string
	}{
		{"prj_01HAA", "env_01HBB", "midnight-proj-01haa-env-01hbb"},
		{"prj-with-dashes", "env-name", "midnight-proj-prjwithdashes-env-envname"},
		{"PRJ_UPPER", "ENV_UPPER", "midnight-proj-upper-env-upper"},
		// Pathological: empty inputs collapse to placeholders rather than
		// producing an invalid Docker name. Real callers will never hit
		// this path because the runtime adapter validates first.
		{"", "", "midnight-proj-x-env-x"},
	}
	for _, tc := range cases {
		t.Run(tc.projectID+"|"+tc.environmentID, func(t *testing.T) {
			pn := ProjectNetwork{ProjectID: tc.projectID, EnvironmentID: tc.environmentID}
			got := pn.Name()
			if got != tc.want {
				t.Fatalf("Name() = %q, want %q", got, tc.want)
			}
			if len(got) > MaxNetworkNameLength {
				t.Fatalf("Name() length = %d, want <= %d", len(got), MaxNetworkNameLength)
			}
		})
	}
}

func TestProjectNetworkNameTruncatesToRFC1123(t *testing.T) {
	pn := ProjectNetwork{
		ProjectID:     strings.Repeat("a", 64),
		EnvironmentID: strings.Repeat("b", 64),
	}
	name := pn.Name()
	if len(name) > MaxNetworkNameLength {
		t.Fatalf("Name() length = %d, want <= %d", len(name), MaxNetworkNameLength)
	}
	if !strings.HasPrefix(name, NetworkNamePrefix+"-") {
		t.Fatalf("Name() = %q, want prefix %q", name, NetworkNamePrefix)
	}
}

func TestServiceAlias(t *testing.T) {
	cases := []struct {
		slug string
		want string
	}{
		{"api", "api.midnight.internal"},
		{"WEB-SVC", "web-svc.midnight.internal"},
		{"  redis  ", "redis.midnight.internal"},
		{"", ""},
	}
	for _, tc := range cases {
		got := ServiceAlias(tc.slug)
		if got != tc.want {
			t.Fatalf("ServiceAlias(%q) = %q, want %q", tc.slug, got, tc.want)
		}
	}
}

func TestServiceAliasesReturnsBothForms(t *testing.T) {
	got := ServiceAliases("api")
	want := []string{"api", "api.midnight.internal"}
	if len(got) != len(want) {
		t.Fatalf("ServiceAliases() = %#v, want %#v", got, want)
	}
	for i, alias := range got {
		if alias != want[i] {
			t.Fatalf("ServiceAliases()[%d] = %q, want %q", i, alias, want[i])
		}
	}
	if got := ServiceAliases(""); got != nil {
		t.Fatalf("ServiceAliases(\"\") = %#v, want nil", got)
	}
}

func TestEnsureNetworkRequiresScope(t *testing.T) {
	p, _ := NewDockerProvisioner(newFakeNetworkClient())
	if _, err := p.EnsureNetwork(context.Background(), "", "env"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("EnsureNetwork(\"\", env) error = %v, want ErrInvalidScope", err)
	}
	if _, err := p.EnsureNetwork(context.Background(), "prj", ""); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("EnsureNetwork(prj, \"\") error = %v, want ErrInvalidScope", err)
	}
}

func TestEnsureNetworkCreatesOnceAndCaches(t *testing.T) {
	fake := newFakeNetworkClient()
	p, err := NewDockerProvisioner(fake)
	if err != nil {
		t.Fatalf("NewDockerProvisioner: %v", err)
	}
	ctx := context.Background()
	id, err := p.EnsureNetwork(ctx, "prj_01", "env_01")
	if err != nil {
		t.Fatalf("first EnsureNetwork: %v", err)
	}
	if id == "" {
		t.Fatal("first EnsureNetwork returned empty id")
	}
	id2, err := p.EnsureNetwork(ctx, "prj_01", "env_01")
	if err != nil {
		t.Fatalf("second EnsureNetwork: %v", err)
	}
	if id != id2 {
		t.Fatalf("idempotent EnsureNetwork: ids differ %q vs %q", id, id2)
	}
	if fake.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (idempotency)", fake.createCalls)
	}
}

func TestEnsureNetworkRediscoversExistingAfterCacheLoss(t *testing.T) {
	fake := newFakeNetworkClient()
	p, _ := NewDockerProvisioner(fake)
	ctx := context.Background()
	first, err := p.EnsureNetwork(ctx, "prj_01", "env_01")
	if err != nil {
		t.Fatalf("first EnsureNetwork: %v", err)
	}
	// Simulate process restart: cache cleared but daemon still has it.
	p.cache = map[string]NetworkID{}
	second, err := p.EnsureNetwork(ctx, "prj_01", "env_01")
	if err != nil {
		t.Fatalf("second EnsureNetwork: %v", err)
	}
	if first != second {
		t.Fatalf("rediscovered id = %q, want %q", second, first)
	}
	if fake.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1 (rediscover, not recreate)", fake.createCalls)
	}
}

func TestEnsureNetworkRecoversFromAlreadyExistsRace(t *testing.T) {
	fake := newFakeNetworkClient()
	fake.createErr = errors.New("Error response from daemon: network with name X already exists")
	// Seed the fake with the network the daemon would have if a parallel
	// EnsureNetwork won the race.
	fake.networks["midnight-proj-01-env-01"] = network.Summary{
		Network: network.Network{
			ID:   "net-existing",
			Name: "midnight-proj-01-env-01",
			Labels: map[string]string{
				LabelManagedBy:     LabelManagedByV,
				LabelProjectID:     "prj_01",
				LabelEnvironmentID: "env_01",
			},
		},
	}
	// First List() must return empty so the code path hits Create().
	fake.suppressListUntilCreate = true
	p, _ := NewDockerProvisioner(fake)
	id, err := p.EnsureNetwork(context.Background(), "prj_01", "env_01")
	if err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if id != "net-existing" {
		t.Fatalf("EnsureNetwork id = %q, want net-existing (race recovery)", id)
	}
}

func TestRemoveNetworkDeletesAllScopedNetworks(t *testing.T) {
	fake := newFakeNetworkClient()
	p, _ := NewDockerProvisioner(fake)
	ctx := context.Background()
	if _, err := p.EnsureNetwork(ctx, "prj_01", "env_01"); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if err := p.RemoveNetwork(ctx, "prj_01", "env_01"); err != nil {
		t.Fatalf("RemoveNetwork: %v", err)
	}
	if fake.removeCalls != 1 {
		t.Fatalf("remove calls = %d, want 1", fake.removeCalls)
	}
	if _, cached := p.cache[scopeKey("prj_01", "env_01")]; cached {
		t.Fatal("cache still contains removed scope")
	}
}

func TestRemoveNetworkIsIdempotent(t *testing.T) {
	fake := newFakeNetworkClient()
	p, _ := NewDockerProvisioner(fake)
	if err := p.RemoveNetwork(context.Background(), "prj_01", "env_01"); err != nil {
		t.Fatalf("RemoveNetwork on empty: %v", err)
	}
}

func TestListManagedNetworksSeedsCache(t *testing.T) {
	fake := newFakeNetworkClient()
	fake.networks["midnight-proj-01-env-01"] = network.Summary{
		Network: network.Network{
			ID:   "net-1",
			Name: "midnight-proj-01-env-01",
			Labels: map[string]string{
				LabelManagedBy:     LabelManagedByV,
				LabelProjectID:     "prj_01",
				LabelEnvironmentID: "env_01",
			},
		},
	}
	p, _ := NewDockerProvisioner(fake)
	list, err := p.ListManagedNetworks(context.Background())
	if err != nil {
		t.Fatalf("ListManagedNetworks: %v", err)
	}
	if len(list) != 1 || list[0].ProjectID != "prj_01" || list[0].EnvironmentID != "env_01" {
		t.Fatalf("ListManagedNetworks = %#v, want one entry for prj_01/env_01", list)
	}
	// Cache must be seeded so subsequent EnsureNetwork is a no-op.
	id, err := p.EnsureNetwork(context.Background(), "prj_01", "env_01")
	if err != nil {
		t.Fatalf("EnsureNetwork after seed: %v", err)
	}
	if id != "net-1" {
		t.Fatalf("seeded id = %q, want net-1", id)
	}
	if fake.createCalls != 0 {
		t.Fatalf("create calls after seed = %d, want 0", fake.createCalls)
	}
}

func TestBuildBuiltinsHasDeterministicOrder(t *testing.T) {
	in := BuiltinsInput{
		ServiceSlug:     "api",
		ProjectID:       "prj_01",
		ProjectSlug:     "demo",
		EnvironmentID:   "env_01",
		EnvironmentName: "production",
		PublicDomain:    "demo-api.midnight.app",
		ReplicaID:       "dep-1-r0",
		DeploymentID:    "dep_99",
	}
	got := BuildBuiltins(in)
	want := []string{
		"MIDNIGHT_DEPLOYMENT_ID=dep_99",
		"MIDNIGHT_ENVIRONMENT_ID=env_01",
		"MIDNIGHT_ENVIRONMENT_NAME=production",
		"MIDNIGHT_PRIVATE_DOMAIN=api.midnight.internal",
		"MIDNIGHT_PROJECT_ID=prj_01",
		"MIDNIGHT_PROJECT_SLUG=demo",
		"MIDNIGHT_PUBLIC_DOMAIN=demo-api.midnight.app",
		"MIDNIGHT_REPLICA_ID=dep-1-r0",
		"MIDNIGHT_SERVICE_NAME=api",
	}
	if len(got) != len(want) {
		t.Fatalf("BuildBuiltins length = %d, want %d. got=%#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BuildBuiltins[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildBuiltinsEmitsEmptyPublicDomainWhenAbsent(t *testing.T) {
	got := BuildBuiltins(BuiltinsInput{ServiceSlug: "api"})
	found := false
	for _, kv := range got {
		if strings.HasPrefix(kv, EnvPublicDomain+"=") {
			found = true
			if kv != EnvPublicDomain+"=" {
				t.Fatalf("public domain = %q, want %s=", kv, EnvPublicDomain)
			}
		}
	}
	if !found {
		t.Fatalf("BuildBuiltins missing %s entry: %#v", EnvPublicDomain, got)
	}
}

func TestMergeUserOverridesAfterPutsBuiltinsFirst(t *testing.T) {
	builtins := []string{"MIDNIGHT_SERVICE_NAME=api"}
	user := []string{"MIDNIGHT_SERVICE_NAME=user-override", "DB_URL=postgres://..."}
	got := MergeUserOverridesAfter(builtins, user)
	want := []string{
		"MIDNIGHT_SERVICE_NAME=api",
		"MIDNIGHT_SERVICE_NAME=user-override",
		"DB_URL=postgres://...",
	}
	if len(got) != len(want) {
		t.Fatalf("merge length = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("merge[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// fakeNetworkClient is the minimal in-memory DockerNetworkClient used by
// the provisioner tests. Counters expose call counts so assertions can
// pin idempotency invariants.
type fakeNetworkClient struct {
	mu                      sync.Mutex
	networks                map[string]network.Summary
	createCalls             int
	removeCalls             int
	listCalls               int
	createErr               error
	listErr                 error
	suppressListUntilCreate bool
	listsBeforeCreate       int
}

func newFakeNetworkClient() *fakeNetworkClient {
	return &fakeNetworkClient{networks: make(map[string]network.Summary)}
}

func (f *fakeNetworkClient) NetworkCreate(_ context.Context, name string, options dockerclient.NetworkCreateOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	if f.createErr != nil {
		return "", f.createErr
	}
	if _, ok := f.networks[name]; ok {
		return "", fmt.Errorf("network %q already exists", name)
	}
	id := fmt.Sprintf("netid-%s-%d", name, f.createCalls)
	f.networks[name] = network.Summary{
		Network: network.Network{ID: id, Name: name, Labels: options.Labels},
	}
	return id, nil
}

func (f *fakeNetworkClient) NetworkRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removeCalls++
	for name, n := range f.networks {
		if n.ID == id {
			delete(f.networks, name)
			return nil
		}
	}
	return errors.New("no such network")
}

func (f *fakeNetworkClient) NetworkList(_ context.Context, labelFilter map[string]string) ([]network.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.suppressListUntilCreate && f.listsBeforeCreate == 0 && f.createCalls == 0 {
		f.listsBeforeCreate++
		return nil, nil
	}
	out := make([]network.Summary, 0)
	for _, n := range f.networks {
		match := true
		for k, v := range labelFilter {
			if n.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, n)
		}
	}
	return out, nil
}
