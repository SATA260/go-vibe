package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-migrate/migrate/v4"
	sqlitemigrate "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	"vibe-kanban-go/internal/msgstore"
	"vibe-kanban-go/internal/server/routes"
	"vibe-kanban-go/internal/services/container"
	"vibe-kanban-go/internal/worktree"
)

const (
	defaultAddr = ":8080"
	defaultDB   = "data/go-vibe.db"
)

// main 是服务进程入口，负责创建日志器并启动 HTTP 服务。
func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server exited", "error", err)
		os.Exit(1)
	}
}

// run 完成服务启动主流程：读取配置、打开 SQLite、执行迁移、组装路由和业务服务，最后监听 HTTP 端口。
func run(logger *slog.Logger) error {
	addr := envOrDefault("GO_VIBE_ADDR", defaultAddr)
	dbPath := envOrDefault("GO_VIBE_DB", defaultDB)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=on")
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrateUp(db); err != nil {
		return fmt.Errorf("migrate sqlite: %w", err)
	}

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(60 * time.Second))
	router.Use(cors)

	stores := msgstore.NewRegistry()
	worktrees := worktree.NewManager("worktrees")
	containerService := container.NewService(db, stores, worktrees)

	router.Route("/api", func(r chi.Router) {
		routes.RegisterHealth(r)
		routes.RegisterReal(r, containerService)
		routes.RegisterMock(r, containerService)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("go-vibe server listening", "addr", addr, "db", dbPath)
	return server.ListenAndServe()
}

// cors 允许本地 Vite 前端跨端口访问 Go API，开发期先放开常用方法和请求头。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// migrateUp 执行 SQLite 迁移，确保 API 对外服务前已有 M0/M1a 所需表结构。
func migrateUp(db *sql.DB) error {
	driver, err := sqlitemigrate.WithInstance(db, &sqlitemigrate.Config{})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithDatabaseInstance(
		"file://internal/db/migrations",
		"sqlite3",
		driver,
	)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// envOrDefault 读取环境变量；未设置时返回默认值。
func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
