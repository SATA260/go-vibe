package msgstore

import (
	"context"
	"encoding/json"
	"sync"
)

const (
	KindStdout   LogKind = "stdout"
	KindStderr   LogKind = "stderr"
	KindReady    LogKind = "ready"
	KindFinished LogKind = "finished"
)

type LogKind string

type LogMsg struct {
	Kind LogKind `json:"kind"`
	Data string  `json:"data,omitempty"`
	Seq  int     `json:"seq,omitempty"`
}

type storedMsg struct {
	msg   LogMsg
	bytes int
}

type MsgStore struct {
	mu       sync.RWMutex
	history  []storedMsg
	bytes    int
	maxBytes int
	subs     map[uint64]chan LogMsg
	nextSub  uint64
}

// New 创建一个带内存历史和订阅表的 MsgStore，默认最多保留约 8MB 日志。
func New() *MsgStore {
	return &MsgStore{
		maxBytes: 8 * 1024 * 1024,
		subs:     make(map[uint64]chan LogMsg),
	}
}

// Push 写入一条日志消息：先维护有上限的历史，再非阻塞广播给当前订阅者。
// 业务逻辑对齐原 harness：晚连接的前端可以回放近期历史，已连接的前端能实时收到 stdout/stderr/status。
func (s *MsgStore) Push(msg LogMsg) {
	msgBytes := approxBytes(msg)

	s.mu.Lock()
	for s.bytes+msgBytes > s.maxBytes && len(s.history) > 0 {
		dropped := s.history[0]
		s.history = s.history[1:]
		s.bytes -= dropped.bytes
	}
	s.history = append(s.history, storedMsg{msg: msg, bytes: msgBytes})
	s.bytes += msgBytes

	subs := make([]chan LogMsg, 0, len(s.subs))
	for _, ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

// PushStdout 写入一行标准输出日志。
func (s *MsgStore) PushStdout(line string) {
	s.Push(LogMsg{Kind: KindStdout, Data: line})
}

// PushStderr 写入一行标准错误日志。
func (s *MsgStore) PushStderr(line string) {
	s.Push(LogMsg{Kind: KindStderr, Data: line})
}

// PushReady 标记执行进程已经启动，前端可以开始展示日志流。
func (s *MsgStore) PushReady() {
	s.Push(LogMsg{Kind: KindReady})
}

// PushFinished 标记执行进程日志流结束，SSE 订阅者收到后可以关闭连接。
func (s *MsgStore) PushFinished() {
	s.Push(LogMsg{Kind: KindFinished})
}

// Subscribe 返回当前历史、实时日志 channel 和取消函数；调用方必须在结束时 cancel 释放订阅。
func (s *MsgStore) Subscribe() ([]LogMsg, <-chan LogMsg, func()) {
	s.mu.Lock()
	history := make([]LogMsg, 0, len(s.history))
	for _, item := range s.history {
		history = append(history, item.msg)
	}

	id := s.nextSub
	s.nextSub++
	ch := make(chan LogMsg, 128)
	s.subs[id] = ch
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		if sub, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(sub)
		}
		s.mu.Unlock()
	}

	return history, ch, cancel
}

// SubscribeLive 只订阅调用之后产生的实时日志，不回放内存历史。
// SSE 在先订阅、再查询 DB 历史时使用它，并依靠日志 seq 去重，避免重启回放和实时流混在一起。
func (s *MsgStore) SubscribeLive() (<-chan LogMsg, func()) {
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan LogMsg, 128)
	s.subs[id] = ch
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		if sub, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(sub)
		}
		s.mu.Unlock()
	}

	return ch, cancel
}

// HistoryPlusStream 按原 vibe-kanban 语义先发送历史日志，再持续转发实时日志，直到 context 取消或订阅结束。
// 业务上允许前端稍晚连接 SSE 时仍能看到已产生的 ready/stdout 事件。
func (s *MsgStore) HistoryPlusStream(ctx context.Context) <-chan LogMsg {
	out := make(chan LogMsg)
	history, live, cancel := s.Subscribe()

	go func() {
		defer close(out)
		defer cancel()

		for _, msg := range history {
			select {
			case out <- msg:
			case <-ctx.Done():
				return
			}
		}

		for {
			select {
			case msg, ok := <-live:
				if !ok {
					return
				}
				select {
				case out <- msg:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}

// approxBytes 粗略估算日志消息大小，用于控制 MsgStore 的历史内存上限。
func approxBytes(msg LogMsg) int {
	raw, err := json.Marshal(msg)
	if err != nil {
		return len(msg.Kind) + len(msg.Data) + 16
	}
	return len(raw)
}
