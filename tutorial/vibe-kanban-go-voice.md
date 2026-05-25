# 语音协作设计（播报 + 陪跑）

> 配套文档：[vibe-kanban-go-design.md](vibe-kanban-go-design.md)、[vibe-kanban-go-multi-agent.md](vibe-kanban-go-multi-agent.md)
>
> 单人使用、本地运行。语音作为"调度 + 陪跑"通道，不替代 kanban UI。

---

## 1. 定位与核心决定

- **被动播报**：coding agent 状态变化时主动说出来（跑完了 / 失败了 / 等审批 / 并行结果）
- **主动陪跑**：用户随时说话，能软打断当前 turn 注入新指令、批准 / 拒绝工具调用、查询状态
- **不念代码**：所有播报基于摘要，diff/log 永远在 UI 看
- **单人本地**：不考虑 SFU、多用户、并发会话

### 关键架构决定

**自建管道，不绑定 Realtime 一体化 API**。原因：
1. ASR/TTS/VAD 分别可换（OpenAI / Whisper.cpp / Deepgram / 本地 piper / 系统 TTS）
2. LLM 大脑可换（GPT-4o / Claude / 本地 Qwen / 走 Eino）
3. 成本可控：idle 时不付钱，省一档可用 PTT + 本地 ASR 全离线
4. 业务逻辑（什么时候说、说什么）必须在我们 server，不在第三方黑盒

代价：延迟比 OpenAI Realtime 端到端高 200–500ms，barge-in 需自己做。单人陪跑可接受。

---

## 2. 总体数据流

```
┌──────────────────────────────────────────────────────────────────┐
│ Browser                                                          │
│  ┌──────────────────────────┐                                    │
│  │ Voice Panel (React)      │                                    │
│  │  PeerConnection          │                                    │
│  │  ├── outbound: mic       │ ──── opus over RTP ──┐             │
│  │  └── inbound:  speaker   │ ◄─── opus over RTP ──┤             │
│  │  WebSocket /voice/sig    │ ◄── SDP/ICE/ctrl ───┐│             │
│  └──────────────────────────┘                     ││             │
└────────────────────────────────────────────────────┼┼─────────────┘
                                                    ▼▼
┌──────────────────────────────────────────────────────────────────┐
│ Go server                                                        │
│                                                                  │
│  ┌────────────────────────┐    ┌──────────────────────────────┐  │
│  │ SignalingHub (WS)      │    │ MediaSession (Pion WebRTC)   │  │
│  │  - SDP offer/answer    │◄──►│  - inbound audio track       │  │
│  │  - ICE trickle         │    │  - outbound audio track      │  │
│  │  - reconnect tokens    │    │  - data channel "ctrl"       │  │
│  └────────────────────────┘    └──────────────┬───────────────┘  │
│                                               │  PCM/opus        │
│                  ┌────────────────────────────┼────────────────┐ │
│                  ▼                            ▼                │ │
│            ┌──────────┐  speech?      ┌────────────┐          │ │
│            │   VAD    │──────────────►│    ASR     │──text───►│ │
│            └──────────┘               └────────────┘          │ │
│                                                                ▼ │
│                                              ┌──────────────────┴┐│
│                                              │ VoiceOrchestrator ││
│                                              │  - LLM 大脑        ││
│                                              │  - 工具调用        ││
│                                              │  - 决策何时播报     ││
│                                              └─────┬─────────┬───┘│
│                                                    │ tools   │text│
│                       ┌────────────────────────────┘         │    │
│                       ▼                                      ▼    │
│            ┌──────────────────────┐                  ┌──────────┐ │
│            │ Container / Conductor│                  │   TTS    │ │
│            │ MsgStore events      │──events to───►   └────┬─────┘ │
│            │ Approvals queue      │  Orchestrator        │ pcm    │
│            └──────────────────────┘                       │       │
│                                                           ▼       │
│                                                MediaSession.Send  │
└──────────────────────────────────────────────────────────────────┘
```

---

## 3. 三大组件抽象

ASR / TTS / VAD 各定义一个接口；首发各两个实现，证明抽象到位。

### 3.1 VAD

```go
package voice

// VAD 流式检测语音段。输入 PCM 帧（16kHz mono int16）
type VAD interface {
    Name() string
    // Process 喂一帧 20ms PCM，返回事件流（可能空）
    Process(frame []int16) []VADEvent
    // Reset 清状态（用户切换会话）
    Reset()
    Close() error
}

type VADEvent struct {
    Kind     VADKind   // SpeechStart / SpeechEnd / Silence
    AtMs     int64     // 帧序对应毫秒
}

type VADKind int
const (
    VADSpeechStart VADKind = iota
    VADSpeechEnd
)
```

**首发实现**：
- `silero`（推荐默认）：Silero VAD ONNX，本地跑，单帧 1ms，识别率好
- `webrtcvad`：`go-webrtcvad` 绑定，aggressiveness 三档，超轻量
- 后续：`energy` 简单能量阈值（兜底）

VAD 结果驱动 ASR 切片。`SpeechStart` 开始 buffer，`SpeechEnd` 把 buffer flush 给 ASR。期间也可走 streaming ASR 边讲边出，看实现选。

### 3.2 ASR

```go
type ASR interface {
    Name() string
    // OpenStream 开一条流式识别。返回写 PCM 的 sink 和读 partial/final 的 chan
    OpenStream(ctx context.Context, opts ASROptions) (ASRStream, error)
    // OneShot 一次性识别（兜底；调用 OpenStream 然后 close）
    OneShot(ctx context.Context, pcm []int16, opts ASROptions) (string, error)
}

type ASROptions struct {
    Lang        string // "zh" / "en" / "auto"
    SampleRate  int    // 16000
    Hints       []string // biased vocab：仓库名、agent 名、命令名
}

type ASRStream interface {
    Push(frame []int16) error
    Events() <-chan ASREvent
    Close() error
}

type ASREvent struct {
    Kind    ASRKind  // Partial / Final / Error
    Text    string
    AtMs    int64
    Stable  bool     // partial 是否稳定（可早点行动）
    Err     error
}
```

**首发实现**：
- `whisper_cpp`：本地 whisper.cpp 子进程或 cgo 绑定。选 small / medium，中文够用
- `openai`：OpenAI realtime ASR endpoint 或 Whisper API（非流式兜底）
- 后续：`deepgram`、`paraformer`（阿里）、`fasterwhisper` server

streaming：whisper.cpp 用滑动窗口；openai 用 ws；接口都吐 partial+final。

### 3.3 TTS

```go
type TTS interface {
    Name() string
    // Speak 把文本变成 PCM 流，返回 chan 让调用方串流播放
    Speak(ctx context.Context, req TTSRequest) (<-chan TTSChunk, error)
    Voices(ctx context.Context) ([]Voice, error)
}

type TTSRequest struct {
    Text       string
    Voice      string  // provider-specific
    Lang       string
    SampleRate int     // 期望输出，默认 24000
    Style      string  // 可选：calm / urgent / whisper
}

type TTSChunk struct {
    PCM     []int16  // 一帧或多帧
    Final   bool
    Err     error
}
```

**首发实现**：
- `piper`：本地 piper TTS，<300ms 启动，离线
- `openai`：tts-1 / gpt-4o-mini-tts，质量好但有延迟和钱
- 后续：`elevenlabs`、`azure`、`xtts`

抽象统一吐 PCM；Pion 那边再编码成 Opus 推 RTP。不让各 provider 直接吐 mp3，避免在播放端再解一次。

### 3.4 Provider Registry + 配置

```go
type Providers struct {
    VAD VAD
    ASR ASR
    TTS TTS
}

// config.toml
[voice]
enabled = true
vad     = "silero"
asr     = "whisper_cpp"
tts     = "piper"
[voice.whisper_cpp]
model = "ggml-medium-q5_0.bin"
[voice.piper]
voice = "zh_CN-huayan-medium"
[voice.openai]
api_key_env = "OPENAI_API_KEY"
```

每个 provider 自己注册 factory，启动时按 config 选。换 provider 不重启进程：保留 admin 接口热切（V2 再做）。

---

## 4. WebRTC + WebSocket 信令

### 4.1 为什么 WS 信令

- HTTP POST /offer + /answer 模型只能跑一次，**重连必须重发 offer**，trickle ICE 不方便
- WS 双向、长连，trickle ICE 即时下发，PC 端 `iceCandidate` 一拿到就推给 server
- 我们还要走 WS 推业务消息（"开始录音了" / "TTS 中断" / "session 失效请重连"），data channel 也行但 WS 简单一致

WS 仅做信令 + 控制；音频走 WebRTC。

### 4.2 信令协议

`GET /api/voice/signal` 升级为 WS。消息格式：

```json
{ "type": "<kind>", "id": "<uuid>", ... }
```

| type                       | 方向     | payload                                              |
|----------------------------|----------|------------------------------------------------------|
| `hello`                    | C→S      | `{ resume_token?: string, client_caps: {...} }`     |
| `welcome`                  | S→C      | `{ session_id, ice_servers, resumed: bool }`         |
| `offer`                    | C→S      | `{ sdp }`                                           |
| `answer`                   | S→C      | `{ sdp }`                                           |
| `ice`                      | 双向     | `{ candidate, sdpMid, sdpMLineIndex }`              |
| `ice_restart_offer`        | C→S      | `{ sdp }` （ICE 重协商）                              |
| `ice_restart_answer`       | S→C      | `{ sdp }`                                           |
| `ctrl.mute`                | C→S      | `{ muted: bool }`                                   |
| `ctrl.ptt`                 | C→S      | `{ pressed: bool }` push-to-talk 模式                |
| `ctrl.barge_in`            | C→S      | `{}` 用户按"立刻停"按钮                               |
| `evt.tts_started`          | S→C      | `{ utterance_id }`                                  |
| `evt.tts_done`             | S→C      | `{ utterance_id, interrupted: bool }`               |
| `evt.asr_partial`          | S→C      | `{ text }`                                          |
| `evt.asr_final`            | S→C      | `{ text }`                                          |
| `evt.session_invalid`      | S→C      | `{ reason }` 强制全量重连                              |
| `ping` / `pong`            | 双向     | 25s 心跳                                            |

### 4.3 信令握手时序

```
C: ws connect
C: hello { resume_token: null }
S: welcome { session_id: "v-abc", ice_servers, resumed: false }
C: offer { sdp }
S: answer { sdp }
C: ice (×N, trickle)
S: ice (×N)
… RTP 流跑起来 …
```

ICE servers：单机本地通常 host candidate 直连就够；如果浏览器 → 容器 / 跨网段，挂一个 coturn。

### 4.4 重连机制（核心难点）

三类故障，处理粒度不同：

#### 类型 1：WS 短暂掉线（网络抖动）
- 客户端检测：`onclose` 或 `pong` 超时
- 行为：指数退避重连（200ms / 500ms / 1s / 2s / 5s，capped），同时 PeerConnection **保持不动**
- 重连后发 `hello { resume_token }`，server 用 token 找回原 session
- `welcome { resumed: true }`，跳过 SDP 协商
- 期间累积的 server→client 事件（最近 N 秒）回放给客户端

服务端为每个 session 维护一个小 ring buffer（如最近 5 秒事件）支持回放。

#### 类型 2：ICE 链路失败（NAT 重新绑定 / 网络切换）
- 监听 `pc.oniceconnectionstatechange === 'failed' / 'disconnected'`
- 等 3 秒确认不是瞬断 → 触发 ICE restart：`pc.createOffer({ iceRestart: true })` → 发 `ice_restart_offer`
- server 端 Pion 用 `peerConnection.SetRemoteDescription` + 新答案
- 不重建 PC，不重新订阅业务，音频中断 1–3 秒

#### 类型 3：PC 完全失效 / session 过期 / server 重启
- 服务端发 `evt.session_invalid` 或 WS 最终关闭
- 客户端关闭 PC，新建 PeerConnection，走完整握手
- VoiceOrchestrator 的对话历史保留在 server（绑定 session_id）；如果 session_id 也丢了，重新开始

### 4.5 重连状态机（客户端）

```
                    ┌────────────┐
                    │ Disconnected│
                    └──────┬──────┘
                           │ user enables voice
                           ▼
                    ┌────────────┐
            ┌──────►│ Connecting │
            │       └──────┬─────┘
            │              │ welcome
            │              ▼
            │       ┌────────────┐  WS drop      ┌──────────────┐
            │       │ Connected  │──────────────►│ WSReconnect  │──┐
            │       └─────┬──────┘               └──────┬───────┘  │
            │             │  ICE failed                 │ welcome  │
            │             ▼                             │ (resumed)│
            │       ┌────────────┐                      └──────────┘
            │       │ ICERestart │
            │       └─────┬──────┘
            │             │ failed too many times
            │             ▼
            │       ┌────────────┐
            └───────│ ColdRestart │ session_invalid
                    └────────────┘
```

每个状态超时 → 升级到下一档。客户端 UI 显示对应小状态点（绿/黄/红）。

### 4.6 服务端 session 管理

```go
type VoiceSession struct {
    ID          string
    ResumeToken string         // 给客户端，重连用，服务端比对
    PC          *webrtc.PeerConnection
    InboundTrack  *webrtc.TrackRemote
    OutboundTrack *webrtc.TrackLocalStaticSample
    Pipeline    *AudioPipeline  // VAD/ASR/TTS 实例
    Orchestrator *VoiceOrchestrator
    EventRing   *RingBuffer     // 重连回放
    LastSeen    time.Time
    cancelCtx   context.CancelFunc
}

type SessionManager struct {
    mu       sync.Mutex
    sessions map[string]*VoiceSession
    // 单人单会话：一个 user 同一时刻最多一个
    perUser  map[string]string  // userID -> sessionID
}

// 没收到任何流量 60s 后清理；等到 5min 不重连彻底删
const SessionGraceDuration = 5 * time.Minute
```

---

## 5. 音频流水线

PCM 在管道里走 16kHz mono int16，方便 VAD/ASR；TTS 输出按目标采样率重采样。

### 5.1 入站：mic → ASR

```go
type AudioPipeline struct {
    vad VAD
    asr ASR
    onTranscript func(text string, final bool)
    speaking     atomic.Bool   // 是否正在说话（VAD 判定）
    asrStream    ASRStream
}

func (p *AudioPipeline) IngestRTP(track *webrtc.TrackRemote) {
    decoder := opus.NewDecoder(16000, 1)
    for {
        pkt, _, err := track.ReadRTP()
        if err != nil { return }
        pcm := decoder.Decode(pkt.Payload)        // → []int16, 20ms = 320 samples

        // 分发给 VAD
        events := p.vad.Process(pcm)
        for _, ev := range events {
            switch ev.Kind {
            case VADSpeechStart:
                p.speaking.Store(true)
                stream, _ := p.asr.OpenStream(ctx, ASROptions{...})
                p.asrStream = stream
                go p.consumeASR(stream)
                p.onBargeIn() // 见 §6
            case VADSpeechEnd:
                p.speaking.Store(false)
                p.asrStream.Close()
            }
        }

        if p.speaking.Load() && p.asrStream != nil {
            p.asrStream.Push(pcm)
        }
    }
}
```

ASR 的 partial 转给 orchestrator 用于 barge-in 判断；final 触发真正的"用户消息"事件。

### 5.2 出站：TTS → speaker

```go
func (s *VoiceSession) Speak(ctx context.Context, text string) (utteranceID string) {
    uid := uuid.NewString()
    s.notify(WSMsg{Type: "evt.tts_started", ID: uid})
    s.currentUtterance.Store(uid)

    chunks, _ := s.tts.Speak(ctx, TTSRequest{Text: text, ...})
    enc := opus.NewEncoder(48000, 1)
    resampler := newResampler(24000, 48000)

    interrupted := false
    for c := range chunks {
        if s.currentUtterance.Load() != uid {
            interrupted = true
            break
        }
        opusFrames := enc.Encode(resampler.Process(c.PCM))
        for _, f := range opusFrames {
            s.outboundTrack.WriteSample(media.Sample{Data: f, Duration: 20 * time.Millisecond})
        }
    }
    s.notify(WSMsg{Type: "evt.tts_done", ID: uid, Interrupted: interrupted})
    return uid
}
```

`currentUtterance` 是个 atomic.Value，barge-in 时改写它，正在跑的 Speak goroutine 自己退。

---

## 6. Barge-in（打断）

两层：

### 6.1 浅打断（VAD-based）
用户开口说话 → VAD 触发 SpeechStart → 立即停 TTS（改写 `currentUtterance`），但不一定打断 coding agent。orchestrator 拿到 ASR 结果再决定。

### 6.2 深打断（语义 / 显式）
用户说"等等！"或按 barge_in 按钮 → orchestrator 调用 `inject_user_message` 工具 → 进 attempt 的 follow-up 队列。

如何区分浅 vs 深？让 LLM 判断：

```
system: 用户在 coding agent 跑的时候说了：「{text}」。
判断这是：
A) 闲聊 / 反应（不需要打断 agent）
B) 给 agent 的修正指令（需要 inject）
C) 直接命令你（执行工具）
```

LLM 输出后决定调哪个工具。错判可接受——单人陪跑容错率高。

---

## 7. VoiceOrchestrator 大脑

承担"接收事件 → 决策 → 调用 LLM → 调用工具 / 说话"。

```go
type VoiceOrchestrator struct {
    llm        LLMClient        // OpenAI / Anthropic / Eino-runnable
    tts        TTS
    tools      ToolRegistry
    history    []ChatMessage    // 滑窗
    container  *ContainerService
    eventTaps  *EventTaps       // 订阅 MsgStore + DB events
    speakQueue chan SpeakItem
}

func (o *VoiceOrchestrator) Run(ctx context.Context, session *VoiceSession) {
    go o.handleASR(ctx, session)
    go o.handleEvents(ctx, session)
    go o.runSpeakQueue(ctx, session)
}
```

### 7.1 事件触发规则（被动播报）

不是每个事件都念。先用规则过滤，过的事件再让 LLM 撰文：

```go
type EventRule struct {
    Match func(Event) bool
    Cooldown time.Duration
    Priority int  // 高优先级抢占低优先级 TTS
}

var rules = []EventRule{
    {Match: isExecCompleted,        Cooldown: 5*time.Second,  Priority: 5},
    {Match: isExecFailed,           Cooldown: 5*time.Second,  Priority: 8},
    {Match: isApprovalRequested,    Cooldown: 0,              Priority: 10},
    {Match: isParallelGroupDone,    Cooldown: 30*time.Second, Priority: 6},
    {Match: isLongSilence,          Cooldown: 10*time.Minute, Priority: 1},
}
```

通过的事件喂给 LLM，prompt 形如："agent X 跑完了，状态 Y。给用户一句话播报，<= 20 字。" 再 TTS。

### 7.2 工具集

```go
read_kanban_state()                      // 当前 task / running 摘要
read_attempt_status(id)                  // 一个 attempt 进度
read_diff_summary(id)                    // 改了 X 文件 / +Y -Z 行
inject_user_message(attempt_id, text)    // 软打断，进 follow-up 队列
stop_attempt(attempt_id)                 // 硬停
approve(approval_id, allow bool, reason)
start_attempt(task_id, executor_id, prompt)
create_task(title, prompt, executor)
list_recent_tasks(limit)
ack_notification(notification_id)        // 确认收到通知，不再追问
```

工具实现复用 §4 ContainerService。

### 7.3 LLM 大脑选择

走一个统一 `LLMClient` 接口，支持：
- OpenAI ChatCompletions（gpt-4o-mini 默认；便宜够用）
- Anthropic Messages
- Eino runnable（如果用户已经搭了）

走 Eino 时，orchestrator 本身可以是 Eino ReAct agent。但**MVP 直接用 OpenAI tool calling**，简单稳定。Eino 接入留 V2。

---

## 8. 延迟预算

播报路径目标 < 600ms（事件触发到出声）：
- 事件 → 规则匹配：5ms
- LLM 撰文（短）：200–400ms
- TTS 首块（piper 本地）：80–150ms
- 编码 + 推流：20ms

陪跑路径目标 < 1.5s（用户说完到回应出声）：
- VAD 检测 SpeechEnd：100–300ms（看 silence threshold）
- ASR final：100–500ms（whisper.cpp medium）
- LLM 决策 + 工具调用：300–600ms
- TTS 首块：80–150ms

如果延迟敏感场景（barge-in）：跳过 LLM，直接停 TTS + 注入用户原话。这条 < 300ms。

---

## 9. 数据模型增量

```sql
CREATE TABLE voice_sessions (
    id              TEXT PRIMARY KEY,
    user_id         TEXT,
    resume_token    TEXT NOT NULL,
    started_at      TIMESTAMP NOT NULL,
    ended_at        TIMESTAMP,
    asr_provider    TEXT,
    tts_provider    TEXT,
    vad_provider    TEXT,
    llm_provider    TEXT,
    tokens_in       INTEGER NOT NULL DEFAULT 0,
    tokens_out      INTEGER NOT NULL DEFAULT 0,
    audio_seconds   REAL NOT NULL DEFAULT 0
);

CREATE TABLE voice_utterances (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL REFERENCES voice_sessions(id),
    direction       TEXT NOT NULL,    -- user / assistant
    text            TEXT NOT NULL,
    ts              TIMESTAMP NOT NULL,
    triggered_by    TEXT,             -- event_id / user_speech / barge_in
    tool_calls      JSON
);
```

播报历史可恢复 / 审计 / 调规则。

---

## 10. API / WS 端点增量

```
GET  /api/voice/signal             # WS 升级（信令）
GET  /api/voice/config             # 返回当前 provider 选择 + 可选项
PUT  /api/voice/config             # 切 provider（V2 热切；V1 重启）
GET  /api/voice/sessions/:id       # 历史 + 统计
POST /api/voice/sessions/:id/end   # 显式结束
```

ICE servers 不放在 config 接口里，放在 `welcome` 消息里下发，方便部署时换 turn。

---

## 11. 仓库布局增量

```
internal/voice/
  vad.go              # interface + factory
  asr.go
  tts.go
  registry.go         # config → provider 实例
  vad_silero/
  vad_webrtcvad/
  asr_whispercpp/
  asr_openai/
  tts_piper/
  tts_openai/
  pipeline.go         # AudioPipeline (RTP→PCM→VAD→ASR & TTS→opus→RTP)
  session.go          # VoiceSession + SessionManager
  signaling.go        # WS handler，状态机
  ringbuf.go          # 重连事件回放
  orchestrator.go     # VoiceOrchestrator + 规则
  rules.go
  tools.go            # 暴露给 LLM 的工具
  llm.go              # LLMClient interface
  llm_openai.go
  llm_anthropic.go
  llm_eino.go         # 可选

internal/server/routes/voice.go
```

---

## 12. 落地里程碑

跟主文档 M0–M5 错开排：

| 阶段 | 内容 | 关键验证 |
|------|------|---------|
| **VV0** 文本播报 | 不接 WebRTC。规则触发后用浏览器 `Audio()` 播 server 生成的 mp3 | 规则跑得对不对，什么时候说话比技术更重要 |
| **VV1** Pion + WS 信令 | 浏览器 PC ↔ Pion，能互通 opus；WS hello/offer/answer/ice；echo bot（说啥重复啥）| 音频通路 + 信令稳定 |
| **VV2** 接 VAD/ASR/TTS（各 1 个实现） | 默认 silero + whisper.cpp + piper | 端到端能"你好" → 识别 → "你好啊"回声 |
| **VV3** Orchestrator + 工具 | 实现 8 个核心工具；LLM 用 OpenAI；只读型工具先做 | 单纯问 "现在跑了几个 task" 能正确回答 |
| **VV4** 双向流：陪跑 + barge-in | inject_user_message + 浅/深打断 | 跑长任务时打断成功率 > 90% |
| **VV5** 重连机制 | WS 重连 + ICE restart + cold restart | 拔网线 / 切 wifi / 杀 server 都能恢复 |
| **VV6** 第二个 ASR/TTS 实现 | 接 OpenAI ASR + tts-1，验证抽象到位 | 配置切换不改一行业务代码 |
| **VV7** 成本闸门 + idle | PTT 模式、idle 暂停、token / 时长仪表盘 | 不用时不烧钱 |

VV0 是必经。**单人陪跑最容易失败的不是技术，是"agent 太聒噪让你想关掉"**。规则要在 VV0 用文本+按钮模拟两天才知道怎么调。

---

## 13. 主要风险

- **whisper.cpp 中英文混说差**：默认 medium 模型够用；混说严重时换 paraformer
- **Pion 的 opus 编解码 cgo 依赖**：用 `pion/mediadevices` 或 `hraban/opus`，注意 build 时带 libopus
- **回声 / 啸叫**：浏览器开 `echoCancellation: true` 默认就够；server 端不做 AEC
- **ASR 把 TTS 自己的声音录回来**：浏览器侧 AEC 一般能压；如果不行，TTS 期间 server 端 mute ASR（最简单）
- **LLM 调用阻塞播报队列**：playback queue 单 worker，超过 300ms 没 LLM 响应直接念"稍等"占位
- **server 重启丢 session**：voice_sessions 表保留 resume_token，重启后可在窗口期续上（通过工具历史），但 PC 必须 cold restart
- **桌面浏览器麦克风权限**：HTTPS 才允许。本地用 `mkcert` 起 https，或开 chrome `--unsafely-treat-insecure-origin-as-secure`
- **provider abstraction 漏抽象**：各家 streaming ASR 行为不一致（partial 频率、是否带标点、stable 语义）。接口里 `Stable bool` 可能在某些 provider 始终 false——用法上别强依赖

---

## 14. 不做什么

- 不做 wake word（"嘿 vibe"）：单人本地按键或常开就够，wake word 工程量大且误触发率高
- 不做多说话人分离 / diarization：单人
- 不做远端语音中继（SFU）：单人本地
- 不做声纹认证：本地信任
- 不做实时翻译：可以让 LLM 干，但不是核心场景

---

## 15. 与主设计 / 多 agent 文档的衔接

- **被动播报数据源**：完全复用主设计 §6.2 DB 变更广播 + MsgStore，orchestrator 订阅同样的事件流
- **工具调用底层**：复用 [vibe-kanban-go-design.md](vibe-kanban-go-design.md) §4 ContainerService、§4.5 Conductor、[vibe-kanban-go-multi-agent.md](vibe-kanban-go-multi-agent.md) §3.3 ParallelGroup
- **多 agent 场景下的播报**：parallel group 完成时优先播报，handoff 切换时简短提示（"Claude 跑完，开始 review 阶段"）。这部分规则在 VV3 加
- **Eino executor 跑得起来时**：orchestrator 自己可以也用 Eino，复用同一套 LLM 抽象（`llm_eino.go`）

---

文档到此。先写 VV0 验证规则、再补 VV1 信令骨架，是单人语音陪跑能不能用得舒服的分水岭。