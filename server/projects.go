package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// projectHeader carries the project the web UI is working in. Lists are
// filtered by it and new tasks and chats are created in it; without it every
// project is shown and new items go to the default project.
const projectHeader = "X-Artex-Project"

// requestProject returns the project a request is scoped to (0 = all projects).
// Downloads and event streams cannot set headers, so a project query parameter works too.
func requestProject(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get(projectHeader))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("project"))
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

// createProject resolves the project for something new: an explicit id from the
// body wins, then the request's project, then the default project.
func (s *Server) createProject(r *http.Request, explicit int64) (int64, error) {
	id := explicit
	if id <= 0 {
		id = requestProject(r)
	}
	return s.m.pg.ResolveProject(id)
}

func projectHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrProjectNotFound):
		writeErr(w, 404, err.Error())
	case errors.Is(err, db.ErrProjectInvalid), errors.Is(err, db.ErrProjectDefault):
		writeErr(w, 400, err.Error())
	case errors.Is(err, db.ErrProjectNotEmpty):
		writeErr(w, 409, err.Error())
	default:
		writeErr(w, 500, err.Error())
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.m.pg.ListProjects()
	if err != nil {
		projectHTTPError(w, err)
		return
	}
	if ps == nil {
		ps = []*db.Project{}
	}
	writeJSON(w, 200, map[string]any{"projects": ps})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.m.pg.GetProject(int64(atoiDefault(r.PathValue("id"), 0)))
	if err != nil {
		projectHTTPError(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) postProject(w http.ResponseWriter, r *http.Request) {
	var in db.ProjectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	p, err := s.m.pg.CreateProject(in)
	if err != nil {
		projectHTTPError(w, err)
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	var body struct {
		db.ProjectInput
		Status *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	var (
		p   *db.Project
		err error
	)
	if strings.TrimSpace(body.Name) != "" {
		if p, err = s.m.pg.UpdateProject(id, body.ProjectInput); err != nil {
			projectHTTPError(w, err)
			return
		}
	}
	if body.Status != nil {
		if p, err = s.m.pg.SetProjectStatus(id, *body.Status); err != nil {
			projectHTTPError(w, err)
			return
		}
	}
	if p == nil {
		writeErr(w, 400, "nothing to update: provide name (with the other fields) or status")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := s.m.pg.DeleteProject(int64(atoiDefault(r.PathValue("id"), 0))); err != nil {
		projectHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// moveTasksToProject reassigns tasks (and drops their category, which belongs to the old project).
func (s *Server) moveTasksToProject(w http.ResponseWriter, r *http.Request) {
	projectID := int64(atoiDefault(r.PathValue("id"), 0))
	var body struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	ids, err := parseIDList(body.TaskIDs)
	if err != nil || len(ids) == 0 {
		writeErr(w, 400, "task_ids must list existing task ids")
		return
	}
	n, err := s.m.pg.MoveTasksToProject(ids, projectID)
	if err != nil {
		projectHTTPError(w, err)
		return
	}
	for _, id := range ids {
		if t, ok := s.m.Task(strconv.FormatInt(id, 10)); ok && t != nil {
			t.updateLifecycle(func(st *taskLifecycleState) {
				if st.ProjectID != projectID {
					st.CategoryID, st.CategoryName = nil, ""
				}
				st.ProjectID = projectID
			})
		}
	}
	writeJSON(w, 200, map[string]any{"moved": n})
}

// moveConversationToProject reassigns one chat.
func (s *Server) moveConversationToProject(w http.ResponseWriter, r *http.Request) {
	convID := int64(atoiDefault(r.PathValue("id"), 0))
	var body struct {
		ProjectID int64 `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if err := s.m.pg.MoveConversationToProject(convID, body.ProjectID); err != nil {
		projectHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"moved": true})
}

// callerProject returns the project of the agent run in ctx: the task of a
// planner/worker/main-agent run, or the conversation of a chat run. 0 = not an
// agent run (HTTP handlers), which is not restricted.
func (s *Server) callerProject(ctx context.Context) int64 {
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID > 0 {
		return s.m.pg.TaskProjectID(ri.TaskID)
	}
	if id, ok := strings.CutPrefix(ri.SessionID, "conv-"); ok {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			return s.m.pg.ConversationProjectID(n)
		}
	}
	return 0
}

// taskInCallerProject reports whether an agent run may see a task: agents only
// work inside their own project.
func (s *Server) taskInCallerProject(ctx context.Context, t *Task) bool {
	p := s.callerProject(ctx)
	return p == 0 || t.projectIDOf() == p
}

// errOtherProject is what agents see for tasks of another project; it does not
// confirm that the task exists.
func errOtherProject(id string) string { return "task not found in this project: " + id }
