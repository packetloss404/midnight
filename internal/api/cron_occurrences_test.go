package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/midnight/midnight/internal/deploy"
)

type cronOccurrenceStoreStub struct{ items []deploy.CronOccurrence }

func (s cronOccurrenceStoreStub) ListOccurrences(_ context.Context, projectID, environmentID, serviceID, state string, _ int) ([]deploy.CronOccurrence, error) {
	var out []deploy.CronOccurrence
	for _, item := range s.items {
		if item.ProjectID == projectID && item.EnvironmentID == environmentID && item.ServiceID == serviceID && (state == "" || item.State == state) {
			out = append(out, item)
		}
	}
	return out, nil
}
func (s cronOccurrenceStoreStub) GetOccurrence(_ context.Context, projectID, environmentID, serviceID, id string) (deploy.CronOccurrence, error) {
	for _, item := range s.items {
		if item.ID == id && item.ProjectID == projectID && item.EnvironmentID == environmentID && item.ServiceID == serviceID {
			return item, nil
		}
	}
	return deploy.CronOccurrence{}, pgx.ErrNoRows
}

func TestCronOccurrencesRESTScopesAndExposesDurableFailureEvidence(t *testing.T) {
	ctx := context.Background()
	resources := NewMemoryResourceStore()
	project, _ := resources.CreateProject(ctx, Project{ID: "prj_cron", WorkspaceID: DefaultWorkspaceID, Slug: "cron", DisplayName: "Cron"})
	service, _ := resources.CreateService(ctx, Service{ID: "svc_cron", ProjectID: project.ID, Slug: "job", DisplayName: "Job"})
	envs := NewMemoryEnvironmentStore(nil)
	env, _ := envs.CreateEnvironment(ctx, Environment{ID: "env_cron", ProjectID: project.ID, Slug: "prod", Name: "Prod"})
	exit := 3
	retry := time.Date(2026, 7, 27, 12, 5, 0, 0, time.UTC)
	store := cronOccurrenceStoreStub{items: []deploy.CronOccurrence{{ID: "cron_1", DeploymentID: "dep_1", ProjectID: string(project.ID), ServiceID: string(service.ID), EnvironmentID: string(env.ID), DueAt: retry.Add(-time.Minute), SnapshotSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Image: "registry.example/job@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", State: deploy.CronRunRetryWait, Attempt: 1, MaxAttempts: 2, Timeout: time.Minute, RetryAt: &retry, ExitCode: &exit, FailureCode: "PREDEPLOY_FAILED", FailureMessage: "cron occurrence exited with status 3"}}}
	router := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources, EnvironmentStore: envs, MutationScopeResolver: NewMutationScopeResolver(resources, resources, envs), CronOccurrences: store})
	path := v1alphaPrefix + "/services/" + string(service.ID) + "/environments/" + string(env.ID) + "/cron-occurrences?state=retry_wait"
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rr.Code, rr.Body.String())
	}
	var items []CronOccurrenceView
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RetryAt == nil || items[0].FailureCode != "PREDEPLOY_FAILED" || items[0].ContainerID != "" {
		t.Fatalf("items=%+v", items)
	}
	detail := v1alphaPrefix + "/services/" + string(service.ID) + "/environments/" + string(env.ID) + "/cron-occurrences/cron_1"
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, detail, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCronOccurrencesRESTRejectsInvalidStateAndCrossScopeDetail(t *testing.T) {
	store := cronOccurrenceStoreStub{}
	resources := NewMemoryResourceStore()
	ctx := context.Background()
	project, _ := resources.CreateProject(ctx, Project{ID: "prj_2", WorkspaceID: DefaultWorkspaceID, Slug: "two", DisplayName: "Two"})
	service, _ := resources.CreateService(ctx, Service{ID: "svc_2", ProjectID: project.ID, Slug: "job", DisplayName: "Job"})
	envs := NewMemoryEnvironmentStore(nil)
	env, _ := envs.CreateEnvironment(ctx, Environment{ID: "env_2", ProjectID: project.ID, Slug: "prod", Name: "Prod"})
	router := NewRouterWithDataStack(DataStack{ProjectStore: resources, ServiceStore: resources, EnvironmentStore: envs, MutationScopeResolver: NewMutationScopeResolver(resources, resources, envs), CronOccurrences: store})
	path := v1alphaPrefix + "/services/" + string(service.ID) + "/environments/" + string(env.ID) + "/cron-occurrences?state=wat"
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid state status=%d", rr.Code)
	}
}
