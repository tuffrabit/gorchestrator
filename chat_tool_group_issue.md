I'll investigate this by exploring the project structure and looking for chat drawer, streaming, and agent response handling code.

Let me look at the key files - the app.js frontend, the orchestrator chat.go, and the service.go to understand the event flow.

Now let me look at the chat.go to understand how events are built for the chat thread:

Now I can see the key flow. Let me look at how the runner events are processed in processTurn. The key issue is in this loop starting around line 397:

for ev, err := range r.Run(ctx, userID, sessionID, genai.NewContentFromText(userMsg.Content, genai.RoleUser), agent.RunConfig{}) {
This iterates over events from the runner. The events are processed by checking ev.Content.Role and collecting text or tool calls. But I notice that text content and tool calls are collected into separate arrays/slices (texts for text, addToolMessage for tool calls). Let me trace through this more carefully to understand how they get persisted and rendered.

Let me look at the chat thread partial template to see how it renders:

Now I have a clear picture of the flow. Let me trace through the key code path to confirm my understanding of the bug.

The runner event loop in processTurn (around line 397-425) shows:

var texts []string
for ev, err := range r.Run(ctx, userID, sessionID, ...) {
    // ...
    if ev.Content.Role == genai.RoleModel {
        for _, p := range ev.Content.Parts {
            if p.Text != "" {
                texts = append(texts, p.Text)  // ← accumulated in memory only!
            }
            if p.FunctionCall != nil {
                s.addToolMessage(thread.ID, ...)  // ← persisted immediately to DB
                toolRows++
            }
        }
    }
}

// Finalize happens AFTER the entire loop:
finalize(finalText, "done")  // ← ALL text written at once
Root cause identified. Let me confirm by looking at how finalize works and how the pending message is rendered:

I now have a clear picture of the bug. Let me trace through the code flow to confirm my analysis.

Now I have a complete picture. Let me trace through the flow to confirm:

In processTurn (chat.go lines ~397-425): The runner loop iterates over events from r.Run(). For each event with Role == Model, it checks parts for text and function calls. Text is appended to a local texts []string slice, while tool calls are immediately persisted via addToolMessage() which writes to the DB.

Text is NOT persisted incrementally. The accumulated texts array is only joined and written once the entire runner loop completes (line ~430+):

finalText := strings.Join(texts, "\n")
finalize(finalText, "done")
The pending assistant message shows "Thinking…" while Status == "pending". The stage("Thinking…") callback updates the pending row's content to "Thinking…", which is visible in the chat drawer.

When the turn finishes: All tool calls (persisted one-by-one during the loop) appear first (lower IDs), then the final assistant message appears with all accumulated text at once.

The SSE refresh (refreshChatThread in app.js) fires on each EventChatMessage, but since nothing new is persisted to DB during the thinking/processing phase except tool calls, the frontend only sees chunks of data — first the tool calls as they come in, then all the text at once when finalize() runs.

The bug is clear: text responses are buffered entirely in memory and only written to the database after the entire turn completes, rather than being streamed incrementally to the DB as tokens arrive.

I've found the root cause. Let me trace through the key code path to explain exactly what's happening.

Bug Root Cause
The issue is in internal/orchestrator/chat.go in the processTurn method, specifically in the runner event loop (lines ~397-425):

var texts []string
for ev, err := range r.Run(ctx, userID, sessionID, genai.NewContentFromText(userMsg.Content, genai.RoleUser), agent.RunConfig{}) {
    // ...
    if ev.Content.Role == genai.RoleModel {
        for _, p := range ev.Content.Parts {
            if p.Text != "" {
                texts = append(texts, p.Text)   // ← accumulated in MEMORY only!
            }
            if p.FunctionCall != nil {
                s.addToolMessage(thread.ID, p.FunctionCall.Name, p.FunctionCall.Args)  // ← persisted to DB immediately
                toolRows++
            }
        }
    }
}

// ALL text is written at once AFTER the loop completes:
finalText := strings.Join(texts, "\n")
finalize(finalText, "done")
What's happening:
Tool calls are persisted incrementally — addToolMessage() writes each tool call to the database immediately as it arrives from the runner stream, triggering an SSE event that causes the drawer to refresh and show the new tool call.

Text responses are buffered entirely in memory — The texts []string slice accumulates all text parts during the loop but is never written to the database until the entire turn completes. Only then does finalize(finalText, "done") write everything at once.

The pending message stays as "Thinking…" throughout — The stage("Thinking…") callback sets the assistant message content to "Thinking…" with status "pending". This is visible in the chat drawer until the turn finishes and the pending row is either updated in place (no tool calls) or replaced by a new final row (with tool calls).

The SSE refresh mechanism (refreshChatThread in app.js) fires on each EventChatMessage, but since text isn't persisted during the loop, the frontend only sees incremental updates for tool calls and then ALL the text at once when finalize() runs.

The two symptoms match perfectly:
"Thinking" block visible during queuing — This is the pending assistant message showing "Thinking…" while the runner loop is still accumulating text in memory.
Two chunks instead of interleaved — First, tool calls appear one-by-one (persisted immediately). Then, all the text appears at once when finalize() writes it after the loop completes.
The fix would require:
Writing accumulated text to the pending assistant message incrementally during the runner loop (e.g., periodically calling SetMessageResult on the pending row with the current accumulated text), so that SSE refreshes show streaming text interleaved with tool calls as they arrive in real time, rather than waiting for the entire turn to complete.
