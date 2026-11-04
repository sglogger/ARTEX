//go:build ignore

package db

import (
	"errors"
	"testing"
)

func projectsTestDB(t *testing.T) *DB {
	t.Helper()
	dsn, _, err := DSN()
	if err != nil {
		t.Skip("postgres unavailable")
	}
	d, err := Open(dsn)
	if err != nil {
		t.Skip("postgres unavailable")
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newTestProject(t *testing.T, d *DB, name string) *Project {
	t.Helper()
	p, err := d.CreateProject(ProjectInput{Name: name, Client: name + " AG"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM findings WHERE task_id IN (SELECT id FROM tasks WHERE project_id=$1)`, p.ID)
		rows, _ := d.Query(`SELECT id FROM tasks WHERE project_id=$1`, p.ID)
		var ids []int64
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			_ = d.DeleteTask(id)
		}
		_, _ = d.Exec(`DELETE FROM tasks WHERE project_id=$1`, p.ID)
		_, _ = d.Exec(`DELETE FROM conversations WHERE project_id=$1`, p.ID)
		_, _ = d.Exec(`DELETE FROM projects WHERE id=$1`, p.ID)
	})
	return p
}

func TestDefaultProjectHoldsUnassignedWork(t *testing.T) {
	d := projectsTestDB(t)
	def, err := d.DefaultProjectID()
	if err != nil || def == 0 {
		t.Fatalf("default project missing: %v", err)
	}
	task, err := d.CreateTaskWithOptions("default scoped", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	if task.ProjectID != def {
		t.Errorf("task without project went to %d, want default %d", task.ProjectID, def)
	}
	if err := d.DeleteProject(def); !errors.Is(err, ErrProjectDefault) {
		t.Errorf("default project must not be deletable: %v", err)
	}
	if _, err := d.SetProjectStatus(def, "archived"); !errors.Is(err, ErrProjectDefault) {
		t.Errorf("default project must not be archivable: %v", err)
	}
}

func TestProjectsSeparateTasksCategoriesAndFindings(t *testing.T) {
	d := projectsTestDB(t)
	a := newTestProject(t, d, "Project A")
	b := newTestProject(t, d, "Project B")

	// The same category name may exist in two projects.
	catA, err := d.CreateTaskCategoryInProject(a.ID, "Web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateTaskCategoryInProject(b.ID, "Web"); err != nil {
		t.Fatalf("same category name in another project: %v", err)
	}
	if _, err := d.CreateTaskCategoryInProject(a.ID, "web"); !errors.Is(err, ErrTaskCategoryNameConflict) {
		t.Errorf("duplicate name in one project: %v", err)
	}
	if _, err := d.CreateTaskWithOptions("wrong category", "g", TaskCreateOptions{ProjectID: b.ID, CategoryID: &catA.ID}); !errors.Is(err, ErrTaskCategoryInvalid) {
		t.Errorf("category of another project accepted: %v", err)
	}

	ta, err := d.CreateTaskWithOptions("task A", "g", TaskCreateOptions{ProjectID: a.ID, CategoryID: &catA.ID})
	if err != nil {
		t.Fatal(err)
	}
	tb, err := d.CreateTaskWithOptions("task B", "g", TaskCreateOptions{ProjectID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFinding(ta.ID, 0, "XSS", "finding A", "high", "", "", "w", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFinding(tb.ID, 0, "SQLi", "finding B", "critical", "", "", "w", nil); err != nil {
		t.Fatal(err)
	}

	list, err := d.ListFindingsForExport(FindingFilter{ProjectID: a.ID}, nil)
	if err != nil || len(list) != 1 || list[0].Name != "finding A" {
		t.Fatalf("project A findings: %v %+v", err, list)
	}
	stats, err := d.FindingStatsForProject(b.ID)
	if err != nil || stats.Total != 1 || stats.Critical != 1 {
		t.Fatalf("project B stats: %v %+v", err, stats)
	}
	got, err := d.GetProject(a.ID)
	if err != nil || got.Tasks != 1 || got.Findings.High != 1 {
		t.Fatalf("project A counts: %v %+v", err, got)
	}
	cats, err := d.ListTaskCategoriesForProject(a.ID)
	if err != nil || len(cats) != 1 || cats[0].ProjectID != a.ID {
		t.Fatalf("project A categories: %v %+v", err, cats)
	}

	// A non-empty project cannot be deleted; moving its task out empties it.
	if err := d.DeleteProject(a.ID); !errors.Is(err, ErrProjectNotEmpty) {
		t.Errorf("non-empty project deleted: %v", err)
	}
	if n, err := d.MoveTasksToProject([]int64{ta.ID}, b.ID); err != nil || n != 1 {
		t.Fatalf("move task: %v %d", err, n)
	}
	moved, _ := d.GetTask(ta.ID)
	if moved.ProjectID != b.ID || moved.CategoryID != nil {
		t.Errorf("moved task keeps old project or category: %+v", moved)
	}
	if err := d.DeleteProject(a.ID); err != nil {
		t.Errorf("empty project not deletable: %v", err)
	}
}

func TestConversationsBelongToProjects(t *testing.T) {
	d := projectsTestDB(t)
	p := newTestProject(t, d, "Chat project")
	c, err := d.CreateConversationInProject(p.ID, "auto", "scoped chat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.ProjectID != p.ID || d.ConversationProjectID(c.ID) != p.ID {
		t.Fatalf("chat project: %+v", c)
	}
	list, err := d.ListConversationsForProject(p.ID)
	if err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Fatalf("project chats: %v %+v", err, list)
	}
	if _, err := d.CreateConversationInProject(987654321, "auto", "x", nil); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("unknown project accepted: %v", err)
	}
}
