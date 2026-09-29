package server

import (
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/orchestrator"
)

var (
	reFS  = regexp.MustCompile(`name="flow_state" id="flow-state" value="([^"]*)"`)
	reRow = regexp.MustCompile(`class="flow-row"`)
	reSel = regexp.MustCompile(`<option value="([^"]*)"[^>]*selected`)
)

type simState struct {
	project  string
	flow     string
	selected []string
	rows     int
}

func mkServer(t *testing.T, withDefault bool) *Server {
	tmp := t.TempDir()
	cfg := testConfig(tmp)
	if !withDefault {
		cfg.Projects["empty"] = config.ProjectConfig{}
	}
	eng, err := orchestrator.NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	srv, err := New(eng, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// do mimics a browser action, feeding the rendered flow_state back into the DOM.
func do(t *testing.T, srv *Server, s simState, add bool, removeAt int, pickRow int, pickVal string) simState {
	q := url.Values{}
	q.Set("project", s.project)
	q.Set("flow_state", s.flow)
	if add {
		q.Set("flow_add", "1")
	}
	if removeAt >= 0 {
		q.Set("flow_remove", strconv.Itoa(removeAt))
	}
	if pickRow >= 0 {
		// A changed <select> resends the whole #flow-builder (flow_state plus
		// every flow_agent) with its own value first (htmx appends the
		// triggering element before the hx-include walk), plus its 1-based row
		// index as flow_pick so the handler applies it at that slot.
		agents := make([]string, s.rows)
		copy(agents, s.selected)
		agents[pickRow] = pickVal
		q.Add("flow_agent", pickVal)
		for i, a := range agents {
			if i == pickRow {
				continue
			}
			q.Add("flow_agent", a)
		}
		q.Set("flow_pick", strconv.Itoa(pickRow+1))
	}
	req := httptest.NewRequest(http.MethodGet, "/partials/submit/flow?"+q.Encode(), nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	fs := reFS.FindStringSubmatch(body)
	newFlow := ""
	if len(fs) == 2 {
		newFlow = fs[1]
	}
	var newSel []string
	for _, m := range reSel.FindAllStringSubmatch(body, -1) {
		newSel = append(newSel, m[1])
	}
	return simState{project: s.project, flow: newFlow, selected: newSel, rows: len(reRow.FindAllString(body, -1))}
}

func removePick(m map[int]string, idx int) map[int]string {
	out := map[int]string{}
	for k, v := range m {
		if k < idx {
			out[k] = v
		} else if k > idx {
			out[k-1] = v
		}
	}
	return out
}

// verifySnapshot asserts the current selection is consistent with the expected
// picks map (row -> agent), which is the ground truth maintained by the caller.
var lastOps []string

func verifySnapshot(t *testing.T, s simState, expected map[int]string, step int, when string) {
	t.Helper()
	if s.rows != len(s.selected) {
		t.Fatalf("step %d (%s): rows=%d but %d selected values (flow=%q selected=%v)", step, when, s.rows, len(s.selected), s.flow, s.selected)
	}
	// every row's pick must match expected (empty string = unpicked)
	for row := 0; row < s.rows; row++ {
		got := ""
		if row < len(s.selected) {
			got = s.selected[row]
		}
		want := expected[row]
		if got != want {
			t.Fatalf("step %d (%s): row %d pick %q but got %q; flow=%q selected=%v rows=%d\n  ops=%v",
				step, when, row, want, got, s.flow, s.selected, s.rows, lastOps)
		}
	}
	// no picks outside the row range
	for row := range expected {
		if row < 0 || row >= s.rows {
			t.Fatalf("step %d (%s): expected pick at row %d but only %d rows (flow=%q)", step, when, row, s.rows, s.flow)
		}
	}
}

// TestFuzz_FlowSelections runs random add/remove/pick sequences against the
// real handler, feeding each rendered flow_state back as the next DOM state.
func TestFuzz_FlowSelections(t *testing.T) {
	cases := []struct {
		withDefault bool
		seed        int64
	}{
		{true, 1}, {true, 2}, {false, 3}, {false, 7}, {false, 42},
	}
	for _, wd := range cases {
		srv := mkServer(t, wd.withDefault)
		rng := rand.New(rand.NewSource(wd.seed))
		s := simState{project: "acme", flow: "", selected: nil, rows: 0}
		if !wd.withDefault {
			s.project = "empty"
		}
		// Seed ground truth from the initial drawer render (applies default_flow).
		s = do(t, srv, s, false, -1, -1, "")
		expected := map[int]string{}
		for i, v := range s.selected {
			expected[i] = v
		}

		var opsLog []string
		for step := 0; step < 500; step++ {
			ops := rng.Intn(3)
			for o := 0; o < ops; o++ {
				var op string
				switch {
				case s.rows < 8 && rng.Intn(3) == 0: // ADD at end
					op = "ADD"
					s = do(t, srv, s, true, -1, -1, "")
					expected = appendPick(expected, "")
				case s.rows > 0 && rng.Intn(3) == 0: // REMOVE at idx
					idx := rng.Intn(s.rows)
					op = "REMOVE" + strconv.Itoa(idx)
					// flow_remove is 1-based (matches the button's .Index), so send
					// idx+1 to remove the 0-based row position tracked here.
					s = do(t, srv, s, false, idx+1, -1, "")
					expected = removePick(expected, idx)
				default: // PICK at idx
					if s.rows == 0 {
						continue
					}
					ids := []string{"researcher", "planner", "implementer"}
					idx := rng.Intn(s.rows)
					val := ids[rng.Intn(len(ids))]
					op = "PICK" + strconv.Itoa(idx) + "=" + val
					s = do(t, srv, s, false, -1, idx, val)
					expected[idx] = val
				}
				opsLog = append(opsLog, op)
				lastOps = append(lastOps[:0], opsLog...)
				verifySnapshot(t, s, expected, step, op)
			}
		}
	}
}

func appendPick(m map[int]string, v string) map[int]string {
	m[len(m)] = v
	return m
}
