package msgstore

import "sync"

type Registry struct {
	mu     sync.RWMutex
	stores map[string]*MsgStore
}

// NewRegistry 创建执行进程日志仓库注册表，用 execution_process_id 管理多个 MsgStore。
func NewRegistry() *Registry {
	return &Registry{stores: make(map[string]*MsgStore)}
}

// Create 为指定执行进程创建新的 MsgStore，并覆盖同 id 的旧 store。
func (r *Registry) Create(id string) *MsgStore {
	store := New()
	r.mu.Lock()
	r.stores[id] = store
	r.mu.Unlock()
	return store
}

// Get 按执行进程 id 读取 MsgStore，供 SSE 订阅和 Stop 流程写入 finished。
func (r *Registry) Get(id string) (*MsgStore, bool) {
	r.mu.RLock()
	store, ok := r.stores[id]
	r.mu.RUnlock()
	return store, ok
}

// Delete 从注册表移除指定执行进程的 MsgStore。
func (r *Registry) Delete(id string) {
	r.mu.Lock()
	delete(r.stores, id)
	r.mu.Unlock()
}
