package migrations

import (
	"strings"
	"testing"
)

func TestAgentConversationKeysetPaginationMigrationContract(t *testing.T) {
	upBytes, err := FS.ReadFile("0055_agent_conversation_keyset_pagination.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	for _, required := range []string{
		"idx_agent_conversations_created_id_desc",
		"idx_agent_conversations_project_created_id_desc",
		"idx_agent_conversations_service_created_id_desc",
		"idx_agent_conversations_actor_created_id_desc",
		"idx_agent_conversations_project_service_created_id_desc",
		"idx_agent_conversations_project_actor_created_id_desc",
		"idx_agent_conversations_service_actor_created_id_desc",
		"idx_agent_conversations_project_service_actor_created_id_desc",
		"VALUES (55, 'agent_conversation_keyset_pagination'",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("0055 up migration missing %q", required)
		}
	}

	downBytes, err := FS.ReadFile("0055_agent_conversation_keyset_pagination.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	down := string(downBytes)
	for _, required := range []string{"DROP INDEX IF EXISTS", "version = 55", "name = 'agent_conversation_keyset_pagination'"} {
		if !strings.Contains(down, required) {
			t.Fatalf("0055 down migration missing %q", required)
		}
	}

	manifestBytes, err := FS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(manifestBytes)
	if !strings.Contains(manifest, "- version: 55\n    name: agent_conversation_keyset_pagination") {
		t.Fatal("manifest does not register migration 55")
	}
}
