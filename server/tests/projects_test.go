//go:build ignore

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func projectsTestServer(t *testing.T) (*Server, *db.Project, *db.Project, *Task, *Task) {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skip("postgres unavailable")
	}
	t.Cleanup(func() { m.Close() })
	a, err := m.pg.CreateProject(db.ProjectInput{Name: "Client A"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.pg.CreateProject(db.ProjectInput{Name: "Client B"})
	if err != nil {
		t.Fatal(err)
	}
	ta, err := m.CreateTaskWithOptions("task of A", "goal A", db.TaskCreateOptions{ProjectID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	tb, err := m.CreateTaskWithOptions("task of B", "goal B", db.TaskCreateOptions{ProjectID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, task := range []*Task{ta, tb} {
			id, _ := strconv.ParseInt(task.ID, 10, 64)
			_ = m.pg.DeleteTask(id)
		}
		_, _ = m.pg.Exec(`DELETE FROM tasks WHERE project_id IN ($1,$2)`, a.ID, b.ID)
		_, _ = m.pg.Exec(`DELETE FROM conversations WHERE project_id IN ($1,$2)`, a.ID, b.ID)
		_, _ = m.pg.Exec(`DELETE FROM projects WHERE id IN ($1,$2)`, a.ID, b.ID)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(ctx, m, t.TempDir(), t.TempDir(), t.TempDir())
	s.archiveWG.Wait()
	<-s.side.done
	return s, a, b, ta, tb
}

func TestListTasksFollowsProjectHeader(t *testing.T) {
	s, a, _, ta, tb := projectsTestServer(t)
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	r.Header.Set(projectHeader, strconv.FormatInt(a.ID, 10))
	w := httptest.NewRecorder()
	s.listTasks(w, r)
	var out struct {
		Tasks []TaskDTO `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, task := range out.Tasks {
		ids[task.ID] = true
		if task.ProjectID != a.ID {
			t.Errorf("task %s of project %d listed in project %d", task.ID, task.ProjectID, a.ID)
		}
	}
	if !ids[ta.ID] || ids[tb.ID] {
		t.Fatalf("project filter wrong: %v", ids)
	}
}

func TestCreateConversationUsesProjectHeader(t *testing.T) {
	s, a, _, _, _ := projectsTestServer(t)
	r := httptest.NewRequest("POST", "/api/conversations", strings.NewReader(`{"agent_key":"auto","title":"scoped"}`))
	r.Header.Set(projectHeader, strconv.FormatInt(a.ID, 10))
	w := httptest.NewRecorder()
	s.pgCreateConversation(w, r)
	if w.Code != 200 {
		t.Fatalf("create chat: %d %s", w.Code, w.Body.String())
	}
	var c db.Conversation
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if c.ProjectID != a.ID {
		t.Fatalf("chat created in project %d, want %d", c.ProjectID, a.ID)
	}
}

// Agents only see tasks of their own project.
func TestAgentToolsStayInsideTheProject(t *testing.T) {
	s, a, _, ta, tb := projectsTestServer(t)
	conv, err := s.m.pg.CreateConversationInProject(a.ID, "auto", "agent in A", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithRunInfo(context.Background(), agent.RunInfo{SessionID: s.convBusyKey(conv.ID)})
	if got := s.callerProject(ctx); got != a.ID {
		t.Fatalf("caller project %d, want %d", got, a.ID)
	}
	res, err := s.toolListTasks().Call(ctx, json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Flatten(); !strings.Contains(text, `"id":"`+ta.ID+`"`) || strings.Contains(text, `"id":"`+tb.ID+`"`) {
		t.Fatalf("list_tasks leaks other projects: %s", text)
	}
	res, _ = s.toolGetTaskGraph().Call(ctx, json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, tb.ID)), nil)
	if !res.IsError || !strings.Contains(res.Flatten(), "not found in this project") {
		t.Fatalf("get_task_graph reached another project: %s", res.Flatten())
	}
	res, _ = s.toolGetTaskGraph().Call(ctx, json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, ta.ID)), nil)
	if res.IsError {
		t.Fatalf("own project task refused: %s", res.Flatten())
	}
}
