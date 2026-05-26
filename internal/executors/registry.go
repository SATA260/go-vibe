package executors

import (
	"context"
	"sort"
)

type Registry struct {
	items map[string]Executor
}

// NewRegistry 创建执行器注册表，并按 executor id 保存可用执行器。
func NewRegistry(items ...Executor) *Registry {
	registry := &Registry{items: make(map[string]Executor)}
	for _, item := range items {
		registry.Register(item)
	}
	return registry
}

// Register 注册一个执行器；同名 id 会被后注册的执行器覆盖，便于测试和后续配置替换。
func (r *Registry) Register(item Executor) {
	r.items[item.ID()] = item
}

// Get 按 executor id 查找执行器，供 ContainerService 启动 execution 时使用。
func (r *Registry) Get(id string) (Executor, bool) {
	item, ok := r.items[id]
	return item, ok
}

// List 返回所有已注册执行器的可用性摘要，供前端展示可选 executor。
func (r *Registry) List(ctx context.Context) []Info {
	infos := make([]Info, 0, len(r.items))
	for _, item := range r.items {
		availability := item.Availability(ctx)
		infos = append(infos, Info{
			ID:        item.ID(),
			Name:      item.Name(),
			Available: availability.Installed && availability.LoggedIn,
			Detail:    availability.Detail,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ID < infos[j].ID
	})
	return infos
}
