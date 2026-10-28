package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/midnight/midnight/internal/agent"
	agentauth "github.com/midnight/midnight/internal/agent/auth"
	"github.com/midnight/midnight/internal/idempotency"
	"github.com/midnight/midnight/internal/identity"
	"github.com/midnight/midnight/internal/sourceartifact"
	"github.com/midnight/midnight/pkg/orailtypes"
)

type sourceGate bool

func (g sourceGate) AllowMutations(context.Context, agentauth.SessionContext) bool { return bool(g) }

type sourceCoordinatorFixture struct {
	fs         *sourceartifact.FSStore
	preflights []TenantSourceUpPreflightRequest
	accepted   int
	replay     bool
	response   idempotency.StoredResponse
	published  int
	finish     int
	refuse     error
}

func (c *sourceCoordinatorFixture) PreflightSourceUp(_ context.Context, r TenantSourceUpPreflightRequest) (TenantSourceUpAdmission, error) {
	c.preflights = append(c.preflights, r)
	if c.refuse != nil {
		return TenantSourceUpAdmission{}, c.refuse
	}
	return TenantSourceUpAdmission{ReservationID: "reservation", IntentHash: "bound", Replay: c.replay}, nil
}
func (c *sourceCoordinatorFixture) FinalizeSourceUpReplay(context.Context, TenantSourceUpPreflightRequest) (idempotency.StoredResponse, error) {
	return c.response, nil
}
func (c *sourceCoordinatorFixture) PublishSourceUp(ctx context.Context, r TenantSourceUpPublishRequest) (TenantSourceArtifact, error) {
	c.published++
	a, err := c.fs.Commit(ctx, r.Staged)
	return TenantSourceArtifact{Digest: a.Digest, URI: a.URI, StorageKey: a.StorageKey, CompressedBytes: a.CompressedBytes, UncompressedBytes: a.UncompressedBytes, EntryCount: a.EntryCount}, err
}
func (c *sourceCoordinatorFixture) DeploySourceArtifact(_ context.Context, r TenantSourceUpRequest) (TenantDeployIntakeResult, error) {
	c.accepted++
	if r.AgentSource == nil || r.AgentSource.ConversationID != "conv_source" || r.Artifact.Digest != c.preflights[len(c.preflights)-1].ExpectedDigest {
		return TenantDeployIntakeResult{}, errors.New("lost source/conversation lineage")
	}
	body, _ := json.Marshal(upOperationResponse{OperationID: "op_source", DeploymentID: "dep_source", Operation: orailtypes.Operation{ID: "op_source", Kind: "build", State: orailtypes.OperationPending}})
	c.response = idempotency.StoredResponse{StatusCode: http.StatusAccepted, Body: body}
	return TenantDeployIntakeResult{Response: c.response}, nil
}
func (c *sourceCoordinatorFixture) FinishSourceUpAdmission(context.Context, TenantSourceUpAdmission, bool) error {
	c.finish++
	return nil
}

func sourceHumanContext() context.Context {
	p := identity.Principal{Kind: "user", UserID: "usr_source", SessionID: "ses_source"}
	s := identity.Session{ID: p.SessionID, UserID: p.UserID, SessionGeneration: 1}
	ctx := context.WithValue(context.Background(), identityPrincipalContextKey{}, p)
	ctx = context.WithValue(ctx, identitySessionContextKey{}, s)
	return agent.WithSourceConversationID(ctx, "conv_source")
}
func sourceAgentFixture() agent.SourceDeployRequest {
	return agent.SourceDeployRequest{ServiceID: "svc_source", EnvironmentID: "env_source", RequestKey: "request-1", ExpectedServiceVersion: 1, ExpectedEnvironmentVersion: 1, Files: map[string]string{"Dockerfile": "FROM scratch\n", "index.html": "source fixture"}}
}

func TestTenantAgentSourceUsesCanonicalUploadAndStableConversationIdempotency(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("durable source publication requires Unix directory fsync; required Linux tests execute this case")
	}
	fs, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &sourceCoordinatorFixture{fs: fs}
	builds := NewTenantAgentSourceBuilds(DataStack{TenantUpIntake: coordinator, SourceArtifactStore: fs, AgentMutationGate: sourceGate(true)}, "agent:chat").(agent.ScopedBuildIntake)
	request := sourceAgentFixture()
	ctx := sourceHumanContext()
	first, err := builds.DeployScopedSource(ctx, request)
	if err != nil || first.ID != "dep_source" || first.Status != string(orailtypes.OperationQueued) || coordinator.accepted != 1 || coordinator.published != 1 {
		t.Fatalf("canonical source intake failed: %v (cause %v)", err, errors.Unwrap(err))
	}
	coordinator.replay = true
	second, err := builds.DeployScopedSource(ctx, request)
	if err != nil || second.ID != first.ID || coordinator.accepted != 1 || coordinator.published != 1 {
		t.Fatalf("replay rebuilt or reaccepted: %v", err)
	}
	if coordinator.preflights[0].Key != coordinator.preflights[1].Key || coordinator.preflights[0].ExpectedDigest != coordinator.preflights[1].ExpectedDigest {
		t.Fatal("retry changed canonical key/digest")
	}
	coordinator.refuse = identity.ErrAuthorizationDenied
	_, err = builds.DeployScopedSource(agent.WithSourceConversationID(ctx, "conv_other"), request)
	if !errors.Is(err, identity.ErrAuthorizationDenied) || coordinator.preflights[2].Key == coordinator.preflights[0].Key {
		t.Fatal("conversation idempotency alias or lost current refusal")
	}
}

func TestTenantAgentSourceArchiveIsDeterministicAndIntakeRefusesMissingHuman(t *testing.T) {
	request := sourceAgentFixture()
	first, err := agentSourceArchive(request)
	if err != nil {
		t.Fatal(err)
	}
	request.Files = map[string]string{"index.html": "source fixture", "Dockerfile": "FROM scratch\n"}
	second, err := agentSourceArchive(request)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("archive identity depends on map order or clock")
	}
	fs, err := sourceartifact.NewFSStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	staged, err := fs.Stage(context.Background(), bytes.NewReader(first), sourceartifact.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Abort(staged)
	if staged.EntryCount != 2 {
		t.Fatal("inline archive was not accepted by canonical source policy")
	}
	coordinator := &sourceCoordinatorFixture{fs: fs}
	builds := NewTenantAgentSourceBuilds(DataStack{TenantUpIntake: coordinator, SourceArtifactStore: fs, AgentMutationGate: sourceGate(true)}, "agent:chat").(agent.ScopedBuildIntake)
	if _, err := builds.DeployScopedSource(context.Background(), request); !errors.Is(err, ErrAgentMutationNeedsHumanSession) || len(coordinator.preflights) != 0 {
		t.Fatal("operator/missing human reached source preflight")
	}
	ctx, cancel := context.WithDeadline(sourceHumanContext(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := builds.DeployScopedSource(ctx, request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired execution reached intake")
	}
}
