package orchestrator

// Model activity ops for the dashboard chip while the inference lifecycle
// controller is busy for an issue.
const (
	ModelOpWait   = "wait"   // blocked on the exclusive-mode server lock
	ModelOpLoad   = "load"   // EnsureLoaded in flight (model load can take minutes)
	ModelOpUnload = "unload" // UnloadAll in flight
)

// ModelActivity describes in-flight inference lifecycle work for an issue.
// It is tracked in memory only: activity is transient and a restart leaves
// no load/unload in progress, so there is nothing to recover.
type ModelActivity struct {
	Op    string `json:"op"` // ModelOpWait | ModelOpLoad | ModelOpUnload
	Model string `json:"model,omitempty"`
	Phase string `json:"phase,omitempty"`
}

// Label renders the activity for the dashboard chip and SSE message.
func (a ModelActivity) Label() string {
	switch a.Op {
	case ModelOpWait:
		return "waiting for model server"
	case ModelOpLoad:
		return "loading model " + a.Model
	case ModelOpUnload:
		return "unloading model " + a.Model
	default:
		return a.Op
	}
}

func (e *Engine) setModelActivity(issueID int64, act ModelActivity) {
	e.modelActMu.Lock()
	if e.modelActivity == nil {
		e.modelActivity = make(map[int64]ModelActivity)
	}
	e.modelActivity[issueID] = act
	e.modelActMu.Unlock()
}

func (e *Engine) clearModelActivity(issueID int64) {
	e.modelActMu.Lock()
	delete(e.modelActivity, issueID)
	e.modelActMu.Unlock()
}

func (e *Engine) currentModelActivity(issueID int64) (ModelActivity, bool) {
	e.modelActMu.Lock()
	defer e.modelActMu.Unlock()
	act, ok := e.modelActivity[issueID]
	return act, ok
}
