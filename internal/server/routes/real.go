package routes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"vibe-kanban-go/internal/services/container"
)

// RegisterReal 注册真实 M1/M1.5 链路接口：登记 repo、创建 task、启动 workspace，并提供审查查询。
func RegisterReal(r chi.Router, service *container.Service) {
	r.Get("/executors", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, service.ListExecutors(req.Context()))
	})

	r.Get("/repos", func(w http.ResponseWriter, req *http.Request) {
		repos, err := service.ListRepos(req.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, repos)
	})

	r.Get("/repos/structure", func(w http.ResponseWriter, req *http.Request) {
		structure, err := service.ListRepoStructure(req.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, structure)
	})

	r.Post("/repos", func(w http.ResponseWriter, req *http.Request) {
		var body container.CreateRepoRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		repo, err := service.CreateRepo(req.Context(), body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, repo)
	})

	r.Get("/tasks", func(w http.ResponseWriter, req *http.Request) {
		tasks, err := service.ListTasks(req.Context(), req.URL.Query().Get("repo_id"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, tasks)
	})

	r.Post("/tasks", func(w http.ResponseWriter, req *http.Request) {
		var body container.CreateTaskRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		task, err := service.CreateTask(req.Context(), body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, task)
	})

	r.Post("/tasks/{id}/workspaces", func(w http.ResponseWriter, req *http.Request) {
		var body container.StartWorkspaceRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		response, err := service.StartWorkspace(req.Context(), chi.URLParam(req, "id"), body)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})

	r.Get("/workspaces", func(w http.ResponseWriter, req *http.Request) {
		workspaces, err := service.ListWorkspaces(req.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, workspaces)
	})

	r.Get("/workspaces/{id}", func(w http.ResponseWriter, req *http.Request) {
		workspace, err := service.GetWorkspace(req.Context(), chi.URLParam(req, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, workspace)
	})

	r.Get("/workspaces/{id}/diff", func(w http.ResponseWriter, req *http.Request) {
		diff, err := service.GetWorkspaceDiff(req.Context(), chi.URLParam(req, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, diff)
	})

	r.Get("/execution-processes/{id}", func(w http.ResponseWriter, req *http.Request) {
		process, err := service.GetExecutionProcess(req.Context(), chi.URLParam(req, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, process)
	})

	r.Get("/execution-processes/{id}/logs", func(w http.ResponseWriter, req *http.Request) {
		logs, err := service.GetExecutionLogs(req.Context(), chi.URLParam(req, "id"))
		if err != nil {
			writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, logs)
	})
}

// writeServiceError 把服务层的业务错误映射成 HTTP 状态码，未知错误按 500 返回。
func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, container.ErrBadRequest):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, container.ErrRepoNotFound), errors.Is(err, container.ErrTaskNotFound), errors.Is(err, container.ErrWorkspaceNotFound), errors.Is(err, container.ErrExecutionNotFound):
		writeError(w, http.StatusNotFound, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}
