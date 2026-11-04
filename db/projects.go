package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Project bundles tasks, chats and task categories of one engagement. Findings
// belong to it through their task; assets and traffic are shared and filtered
// through the project's tasks.
type Project struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Client      string     `json:"client"`
	Description string     `json:"description"`
	Status      string     `json:"status"` // active | archived
	StartDate   *time.Time `json:"start_date,omitempty"`
	EndDate     *time.Time `json:"end_date,omitempty"`
	IsDefault   bool       `json:"is_default"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`

	// Counts, filled by ListProjects / GetProject.
	Tasks         int `json:"tasks"`
	RunningTasks  int `json:"running_tasks"`
	Conversations int `json:"conversations"`
	Findings      struct {
		Critical int `json:"critical"`
		High     int `json:"high"`
		Medium   int `json:"medium"`
		Low      int `json:"low"`
		Info     int `json:"info"`
	} `json:"findings"`
}

var (
	ErrProjectNotFound = errors.New("project not found")
	ErrProjectInvalid  = errors.New("invalid project")
	ErrProjectNotEmpty = errors.New("the project still has tasks or chats; move or delete them first, or archive the project")
	ErrProjectDefault  = errors.New("the default project cannot be deleted or archived")
)

const projectCols = `p.id, p.name, p.client, p.description, p.status, p.start_date, p.end_date, p.is_default, p.created_at, p.updated_at,
 (SELECT count(*) FROM tasks t WHERE t.project_id=p.id AND t.deleted_at IS NULL),
 (SELECT count(*) FROM tasks t WHERE t.project_id=p.id AND t.deleted_at IS NULL AND t.status='running'),
 (SELECT count(*) FROM conversations c WHERE c.project_id=p.id),
 (SELECT count(*) FILTER (WHERE f.severity='critical') FROM findings f JOIN tasks t ON t.id=f.task_id WHERE t.project_id=p.id),
 (SELECT count(*) FILTER (WHERE f.severity='high') FROM findings f JOIN tasks t ON t.id=f.task_id WHERE t.project_id=p.id),
 (SELECT count(*) FILTER (WHERE f.severity='medium') FROM findings f JOIN tasks t ON t.id=f.task_id WHERE t.project_id=p.id),
 (SELECT count(*) FILTER (WHERE f.severity='low') FROM findings f JOIN tasks t ON t.id=f.task_id WHERE t.project_id=p.id),
 (SELECT count(*) FILTER (WHERE f.severity NOT IN ('critical','high','medium','low')) FROM findings f JOIN tasks t ON t.id=f.task_id WHERE t.project_id=p.id)`

func scanProject(sc interface{ Scan(...any) error }) (*Project, error) {
	p := &Project{}
	err := sc.Scan(&p.ID, &p.Name, &p.Client, &p.Description, &p.Status, &p.StartDate, &p.EndDate, &p.IsDefault, &p.CreatedAt, &p.UpdatedAt,
		&p.Tasks, &p.RunningTasks, &p.Conversations,
		&p.Findings.Critical, &p.Findings.High, &p.Findings.Medium, &p.Findings.Low, &p.Findings.Info)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListProjects returns all projects: the default first, then active ones, then by name.
func (d *DB) ListProjects() ([]*Project, error) {
	rows, err := d.Query(`SELECT ` + projectCols + ` FROM projects p ORDER BY p.is_default DESC, (p.status='archived'), lower(p.name), p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProject returns one project, or ErrProjectNotFound.
func (d *DB) GetProject(id int64) (*Project, error) {
	p, err := scanProject(d.QueryRow(`SELECT `+projectCols+` FROM projects p WHERE p.id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	return p, err
}

// DefaultProjectID returns the id of the default project.
func (d *DB) DefaultProjectID() (int64, error) {
	var id int64
	err := d.QueryRow(`SELECT id FROM projects WHERE is_default`).Scan(&id)
	return id, err
}

// resolveProjectTx maps 0 to the default project and checks that a given id exists.
func resolveProjectTx(q interface {
	QueryRow(string, ...any) *sql.Row
}, id int64) (int64, error) {
	if id <= 0 {
		var def int64
		if err := q.QueryRow(`SELECT id FROM projects WHERE is_default`).Scan(&def); err != nil {
			return 0, fmt.Errorf("default project missing: %w", err)
		}
		return def, nil
	}
	var exists bool
	if err := q.QueryRow(`SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1)`, id).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, ErrProjectNotFound
	}
	return id, nil
}

// ResolveProject is resolveProjectTx outside a transaction.
func (d *DB) ResolveProject(id int64) (int64, error) { return resolveProjectTx(d, id) }

// ProjectInput are the editable fields of a project.
type ProjectInput struct {
	Name        string     `json:"name"`
	Client      string     `json:"client"`
	Description string     `json:"description"`
	StartDate   *time.Time `json:"start_date"`
	EndDate     *time.Time `json:"end_date"`
}

func (in *ProjectInput) normalize() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Client = strings.TrimSpace(in.Client)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" {
		return fmt.Errorf("%w: name is required", ErrProjectInvalid)
	}
	if len([]rune(in.Name)) > 120 {
		return fmt.Errorf("%w: name is limited to 120 characters", ErrProjectInvalid)
	}
	if in.StartDate != nil && in.EndDate != nil && in.EndDate.Before(*in.StartDate) {
		return fmt.Errorf("%w: the end date is before the start date", ErrProjectInvalid)
	}
	return nil
}

// CreateProject inserts a new active project.
func (d *DB) CreateProject(in ProjectInput) (*Project, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	var id int64
	if err := d.QueryRow(`INSERT INTO projects(name, client, description, start_date, end_date) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		in.Name, in.Client, in.Description, in.StartDate, in.EndDate).Scan(&id); err != nil {
		return nil, err
	}
	return d.GetProject(id)
}

// UpdateProject replaces the editable fields.
func (d *DB) UpdateProject(id int64, in ProjectInput) (*Project, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	res, err := d.Exec(`UPDATE projects SET name=$2, client=$3, description=$4, start_date=$5, end_date=$6 WHERE id=$1`,
		id, in.Name, in.Client, in.Description, in.StartDate, in.EndDate)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrProjectNotFound
	}
	return d.GetProject(id)
}

// SetProjectStatus archives or reactivates a project; the default project stays active.
func (d *DB) SetProjectStatus(id int64, status string) (*Project, error) {
	if status != "active" && status != "archived" {
		return nil, fmt.Errorf("%w: status must be active or archived", ErrProjectInvalid)
	}
	p, err := d.GetProject(id)
	if err != nil {
		return nil, err
	}
	if p.IsDefault && status == "archived" {
		return nil, ErrProjectDefault
	}
	if _, err := d.Exec(`UPDATE projects SET status=$2 WHERE id=$1`, id, status); err != nil {
		return nil, err
	}
	return d.GetProject(id)
}

// DeleteProject removes an empty, non-default project; its categories go with it.
func (d *DB) DeleteProject(id int64) error {
	p, err := d.GetProject(id)
	if err != nil {
		return err
	}
	if p.IsDefault {
		return ErrProjectDefault
	}
	var tasks, convs int
	if err := d.QueryRow(`SELECT (SELECT count(*) FROM tasks WHERE project_id=$1), (SELECT count(*) FROM conversations WHERE project_id=$1)`, id).Scan(&tasks, &convs); err != nil {
		return err
	}
	if tasks > 0 || convs > 0 {
		return ErrProjectNotEmpty
	}
	_, err = d.Exec(`DELETE FROM projects WHERE id=$1`, id)
	return err
}

// MoveTasksToProject reassigns tasks; their categories are cleared because
// categories belong to a project.
func (d *DB) MoveTasksToProject(taskIDs []int64, projectID int64) (int64, error) {
	if _, err := d.ResolveProject(projectID); err != nil || projectID <= 0 {
		if err == nil {
			err = ErrProjectNotFound
		}
		return 0, err
	}
	res, err := d.Exec(`UPDATE tasks SET project_id=$2, category_id=CASE WHEN project_id=$2 THEN category_id END
		WHERE id = ANY($1::bigint[]) AND deleted_at IS NULL`, int64ArrayLiteral(taskIDs), projectID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MoveConversationToProject reassigns one chat.
func (d *DB) MoveConversationToProject(convID, projectID int64) error {
	if _, err := d.ResolveProject(projectID); err != nil || projectID <= 0 {
		if err == nil {
			err = ErrProjectNotFound
		}
		return err
	}
	res, err := d.Exec(`UPDATE conversations SET project_id=$2 WHERE id=$1`, convID, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TaskProjectID returns the project of a task (0 when the task is unknown).
func (d *DB) TaskProjectID(taskID int64) int64 {
	var id sql.NullInt64
	_ = d.QueryRow(`SELECT project_id FROM tasks WHERE id=$1`, taskID).Scan(&id)
	return id.Int64
}

// ConversationProjectID returns the project of a chat (0 when unknown).
func (d *DB) ConversationProjectID(convID int64) int64 {
	var id sql.NullInt64
	_ = d.QueryRow(`SELECT project_id FROM conversations WHERE id=$1`, convID).Scan(&id)
	return id.Int64
}

// int64ArrayLiteral renders ids as a Postgres array literal for ANY($n::bigint[]).
func int64ArrayLiteral(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprint(id)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
