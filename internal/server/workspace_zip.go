package server

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// handleWorkspaceZip streams the implementer workspace as a zip archive.
// Available only after the implementation phase result status is "done".
// Auth: viewer+ (same as artifact reads).
func (s *Server) handleWorkspaceZip(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
		return
	}
	view, err := s.eng.GetIssue(r.Context(), id)
	if err != nil || view == nil || view.Issue == nil {
		writeJSONError(w, http.StatusNotFound, "issue not found")
		return
	}
	issue := view.Issue
	ctx := r.Context()

	if !s.implementationDone(ctx, issue.ProjectID, issue.ID) {
		writeJSONError(w, http.StatusConflict, "workspace download is only available after implementation is done")
		return
	}

	ws := storage.WorkspacePath(issue.ProjectID, issue.ID)
	exists, err := s.eng.Store().Exists(ctx, ws)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !exists {
		writeJSONError(w, http.StatusNotFound, "workspace not found")
		return
	}

	filename := fmt.Sprintf("issue-%d-workspace.zip", issue.ID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Cache-Control", "no-store")
	// Do not WriteHeader before streaming — zip.Writer will write body bytes.

	if err := writeWorkspaceZip(ctx, s.eng.Store(), ws, w); err != nil {
		// If nothing was written yet, surface an error; otherwise connection may already be mid-stream.
		return
	}
}

func writeWorkspaceZip(ctx context.Context, store storage.Port, wsRoot string, w http.ResponseWriter) error {
	zw := zip.NewWriter(w)

	files, err := listAllFiles(ctx, store, wsRoot)
	if err != nil {
		_ = zw.Close()
		return err
	}

	type entry struct {
		key string
		rel string
	}
	entries := make([]entry, 0, len(files))
	for _, f := range files {
		rel := strings.TrimPrefix(strings.TrimPrefix(f, wsRoot), "/")
		if rel == "" {
			continue
		}
		entries = append(entries, entry{key: f, rel: rel})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].rel < entries[j].rel
	})

	now := time.Now()
	for _, e := range entries {
		data, err := store.Read(ctx, e.key)
		if err != nil {
			continue
		}
		name := path.Clean(e.rel)
		if name == "." || strings.HasPrefix(name, "../") || name == ".." {
			continue
		}
		hdr := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: now,
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			_ = zw.Close()
			return err
		}
		if _, err := fw.Write(data); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}
