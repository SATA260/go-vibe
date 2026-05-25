package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"vibe-kanban-go/internal/msgstore"
	"vibe-kanban-go/internal/services/container"
)

// RegisterMock 注册 M1a 的流程验证接口：启动 mock 执行、订阅日志流、停止执行进程。
func RegisterMock(r chi.Router, service *container.Service) {
	r.Post("/mock/start", func(w http.ResponseWriter, req *http.Request) {
		response, err := service.StartMock(req.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	})

	r.Get("/events/execution-processes/{id}", func(w http.ResponseWriter, req *http.Request) {
		execID := chi.URLParam(req, "id")
		streamExecutionEvents(w, req, service, execID)
	})

	r.Post("/execution-processes/{id}/stop", func(w http.ResponseWriter, req *http.Request) {
		execID := chi.URLParam(req, "id")
		if err := service.Stop(req.Context(), execID); err != nil {
			if errors.Is(err, container.ErrExecutionNotFound) {
				writeError(w, http.StatusNotFound, err)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

// streamExecutionEvents 将指定 execution_process 的 MsgStore 转成 SSE。
// 业务逻辑是先回放历史日志，再推送实时日志；如果执行不存在，则发送一次 finished 让前端干净收尾。
func streamExecutionEvents(w http.ResponseWriter, req *http.Request, service *container.Service, execID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	store, ok := service.Stores().Get(execID)
	if !ok {
		writeSSE(w, "finished", "")
		flusher.Flush()
		return
	}

	index := 0
	for msg := range store.HistoryPlusStream(req.Context()) {
		writeSSEWithID(w, index, string(msg.Kind), msg.Data)
		flusher.Flush()
		index++
		if msg.Kind == msgstore.KindFinished {
			return
		}
	}
}

// writeSSE 按 text/event-stream 格式写入一条事件，支持多行 data。
func writeSSE(w http.ResponseWriter, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	if data != "" {
		for _, line := range strings.Split(data, "\n") {
			fmt.Fprintf(w, "data: %s\n", line)
		}
	} else {
		fmt.Fprint(w, "data:\n")
	}
	fmt.Fprint(w, "\n")
}

// writeSSEWithID 写入带递增 id 的 SSE 事件，方便前端和调试工具识别顺序。
func writeSSEWithID(w http.ResponseWriter, id int, event, data string) {
	fmt.Fprintf(w, "id: %d\n", id)
	writeSSE(w, event, data)
}

// writeJSON 写入 JSON 响应并设置状态码。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeError 以统一 JSON 形状返回错误信息。
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
