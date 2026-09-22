package sqlite

import (
	"path/filepath"
	"testing"
)

// chatTestRepo opens a temp DB and creates the prerequisite user/project
// rows that the chat_threads foreign keys require.
func chatTestRepo(t *testing.T) (*ChatRepo, int64, int64) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	u, err := NewUserRepo(db).Create("chat@example.com", "Chat User", RoleMember, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewProjectRepo(db).Create("chat-project")
	if err != nil {
		t.Fatal(err)
	}
	return NewChatRepo(db), u.ID, p.ID
}

func TestChatRepo_GetOrCreateThreadIdempotent(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)

	first, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 {
		t.Fatal("first thread has no id")
	}

	again, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("second call returned id %d, want %d", again.ID, first.ID)
	}

	// A different flavor is a distinct thread.
	other, err := chat.GetOrCreateThread(userID, projectID, "researcher", "cheap")
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatalf("flavor=cheap returned id %d, want a new thread", other.ID)
	}

	found, err := chat.FindThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != first.ID {
		t.Fatalf("FindThread = %+v, want thread %d", found, first.ID)
	}
}

func TestChatRepo_GetThreadMissing(t *testing.T) {
	chat, _, _ := chatTestRepo(t)

	thread, err := chat.GetThread(12345)
	if err != nil {
		t.Fatal(err)
	}
	if thread != nil {
		t.Fatalf("GetThread(12345) = %+v, want nil", thread)
	}
}

func TestChatRepo_AddMessageListMessagesOrdering(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)
	thread, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}

	var ids []int64
	for i, content := range []string{"hello", "working on it", "done"} {
		id, err := chat.AddMessage(thread.ID, "assistant", content, "", "done")
		if err != nil {
			t.Fatalf("add message %d: %v", i, err)
		}
		ids = append(ids, id)
	}

	msgs, err := chat.ListMessages(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for i, m := range msgs {
		if m.ID != ids[i] {
			t.Fatalf("message %d id = %d, want %d (ids must be ASC)", i, m.ID, ids[i])
		}
		if m.Role != "assistant" {
			t.Fatalf("message %d role = %q, want assistant", i, m.Role)
		}
	}
	want := []string{"hello", "working on it", "done"}
	for i, m := range msgs {
		if m.Content != want[i] {
			t.Fatalf("message %d content = %q, want %q", i, m.Content, want[i])
		}
	}
}

func TestChatRepo_SetMessageResult(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)
	thread, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}

	id, err := chat.AddMessage(thread.ID, "tool", "", "grep", "running")
	if err != nil {
		t.Fatal(err)
	}

	if err := chat.SetMessageResult(id, "found 3 matches", "done"); err != nil {
		t.Fatal(err)
	}

	msgs, err := chat.ListMessages(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Content != "found 3 matches" {
		t.Fatalf("content = %q, want %q", msgs[0].Content, "found 3 matches")
	}
	if msgs[0].Status != "done" {
		t.Fatalf("status = %q, want done", msgs[0].Status)
	}
	if msgs[0].ToolName != "grep" {
		t.Fatalf("tool_name = %q, want grep", msgs[0].ToolName)
	}
}
