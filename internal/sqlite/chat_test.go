package sqlite

import (
	"fmt"
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

func TestChatRepo_ClearMessagesUpTo(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)
	thread, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}

	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := chat.AddMessage(thread.ID, "user", fmt.Sprintf("m%d", i), "", "done")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	n, err := chat.ClearMessagesUpTo(thread.ID, ids[4])
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("rows deleted = %d, want 5", n)
	}
	msgs, err := chat.ListMessages(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("messages after clear = %d, want 0", len(msgs))
	}
	// The thread row itself must survive: only its messages are removed.
	got, err := chat.GetThread(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != thread.ID {
		t.Fatalf("thread row = %+v, want it still present", got)
	}
}

func TestChatRepo_ClearMessagesWatermarkPreservesNewer(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)
	thread, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}

	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := chat.AddMessage(thread.ID, "user", fmt.Sprintf("m%d", i), "", "done")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	n, err := chat.ClearMessagesUpTo(thread.ID, ids[2])
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("rows deleted = %d, want 3", n)
	}
	msgs, err := chat.ListMessages(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages after watermark clear = %d, want 2", len(msgs))
	}
	if msgs[0].ID != ids[3] || msgs[1].ID != ids[4] {
		t.Fatalf("survivors = [%d %d], want [%d %d]", msgs[0].ID, msgs[1].ID, ids[3], ids[4])
	}
}

func TestChatRepo_ClearMessagesScopedToThread(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)
	a, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := chat.GetOrCreateThread(userID, projectID, "researcher", "cheap")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := chat.AddMessage(a.ID, "user", "a", "", "done"); err != nil {
			t.Fatal(err)
		}
		if _, err := chat.AddMessage(b.ID, "user", "b", "", "done"); err != nil {
			t.Fatal(err)
		}
	}

	maxA, err := chat.MaxMessageID(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.ClearMessagesUpTo(a.ID, maxA); err != nil {
		t.Fatal(err)
	}
	if msgs, err := chat.ListMessages(a.ID); err != nil || len(msgs) != 0 {
		t.Fatalf("thread A messages after clear = %v err=%v, want 0", msgs, err)
	}
	msgs, err := chat.ListMessages(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("thread B messages = %d, want 3 (untouched)", len(msgs))
	}

	if maxB, err := chat.MaxMessageID(b.ID); err != nil || maxB == 0 {
		t.Fatalf("MaxMessageID(B) = %d err=%v, want > 0", maxB, err)
	}
}

func TestChatRepo_MaxMessageIDEmpty(t *testing.T) {
	chat, userID, projectID := chatTestRepo(t)
	thread, err := chat.GetOrCreateThread(userID, projectID, "researcher", "")
	if err != nil {
		t.Fatal(err)
	}
	id, err := chat.MaxMessageID(thread.ID)
	if err != nil || id != 0 {
		t.Fatalf("MaxMessageID(empty) = %d err=%v, want 0 nil", id, err)
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
