package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/config"
	"github.com/tuffrabit/gorchestrator/internal/llm"
	"github.com/tuffrabit/gorchestrator/internal/notify"
	"github.com/tuffrabit/gorchestrator/internal/sqlite"
	"github.com/tuffrabit/gorchestrator/internal/storage"
	"google.golang.org/adk/v2/model"
)

// resolveSessionBudget returns the token ceiling for this issue+provider session.
// Override on the issue wins; else providers map; missing/zero → unlimited (0).
func (e *Engine) resolveSessionBudget(issue *sqlite.Issue, provider string) (ceiling, warnPct int) {
	if provider == "" {
		return 0, 0
	}
	if issue != nil {
		if o := parseBudgetOverrides(issue.BudgetOverridesJSON); o != nil {
			if v, ok := o[provider]; ok && v > 0 {
				// Issue override: absolute ceiling; still use provider warn_pct if configured.
				warn := 80
				if p, ok := e.cfg.ProviderBudget(provider); ok && p.WarnPct > 0 {
					warn = p.WarnPct
				}
				return v, warn
			}
		}
	}
	p, ok := e.cfg.ProviderBudget(provider)
	if !ok || p.TokenBudget <= 0 {
		return 0, 0
	}
	warn := p.WarnPct
	if warn <= 0 {
		warn = 80
	}
	return p.TokenBudget, warn
}

func parseBudgetOverrides(raw string) map[string]int {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	out := map[string]int{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// sumUsageFromEvents totals tokens from usage events in events.jsonl for a phase.
func sumUsageFromEvents(ctx context.Context, store storage.Port, eventsPath string) int {
	data, err := store.Read(ctx, eventsPath)
	if err != nil || len(data) == 0 {
		return 0
	}
	total := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	// Allow long event lines.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev eventRecord
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type == "usage" && ev.Tokens > 0 {
			total += ev.Tokens
		}
	}
	return total
}

// wrapModelWithBudget applies the provider session gate when configured.
func (e *Engine) wrapModelWithBudget(ctx context.Context, issue *sqlite.Issue, project *sqlite.Project, phase, provider string, eventsPath string, dryRun bool, inner model.LLM) model.LLM {
	if dryRun {
		// Still enforce when dryrun provider has a budget (tests); use provider name from modelCfg.
	}
	ceiling, warnPct := e.resolveSessionBudget(issue, provider)
	if ceiling <= 0 {
		return inner
	}
	spent := sumUsageFromEvents(ctx, e.store, eventsPath)
	var onWarn func(spent, ceiling int)
	if warnPct > 0 && project != nil && issue != nil {
		pid, iid, title, pname := issue.ID, issue.ID, issue.Title, project.Name
		_ = pid
		onWarn = func(spent, ceiling int) {
			msg := fmt.Sprintf("token budget warning: issue #%d phase %s provider %s spent %d/%d (%d%%)",
				iid, phase, provider, spent, ceiling, spent*100/ceiling)
			log.Print(msg)
			if e.notifier != nil {
				_ = e.notifier.Send(context.Background(), notify.Notification{
					Kind:      notify.KindInfo,
					Recipient: "admin",
					Subject:   fmt.Sprintf("Budget warning: issue #%d %s", iid, phase),
					Body:      msg + "\nTitle: " + title + "\nProject: " + pname,
					IssueID:   iid,
				})
			}
		}
	}
	return llm.WrapBudget(inner, llm.BudgetConfig{
		Ceiling: ceiling,
		Spent:   spent,
		WarnPct: warnPct,
		OnWarn:  onWarn,
	})
}

// ProviderBudgetConfig re-export helper for tests.
func (e *Engine) providerBudgetForTest(name string) (config.ProviderBudgetConfig, bool) {
	return e.cfg.ProviderBudget(name)
}

// mergeIssueBudgetOverrides merges absolute provider ceilings into the issue row.
func (e *Engine) mergeIssueBudgetOverrides(issueID int64, overrides map[string]int) error {
	issue, err := e.issues.Get(issueID)
	if err != nil || issue == nil {
		return fmt.Errorf("issue %d not found", issueID)
	}
	cur := parseBudgetOverrides(issue.BudgetOverridesJSON)
	if cur == nil {
		cur = map[string]int{}
	}
	for k, v := range overrides {
		k = strings.TrimSpace(k)
		if k == "" || v <= 0 {
			continue
		}
		cur[k] = v
	}
	raw, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return e.issues.SetBudgetOverrides(issueID, string(raw))
}
