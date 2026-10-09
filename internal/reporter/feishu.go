package reporter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jrmarcco/goaw/internal/engine"
	"github.com/jrmarcco/goaw/internal/schema"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"
)

// FeishuBot 封装飞书机器人接口。
type FeishuBot struct {
	appID     string
	appSecret string
	workspace string

	// planMode 飞书会话的创建模式，当前固定为 Plan 模式。
	planMode bool

	agentRunTimeout    time.Duration
	messageSendTimeout time.Duration

	client *lark.Client
	engine *engine.AgentEngine

	// sessions 组合根注入的会话管理器，进程内所有入口共享，
	// 飞书 chatID 与 Agent 会话一一对应。
	sessions *engine.SessionManager
}

// NewFeishuBot 创建飞书机器人。
// eng 与 sessions 均由调用方 ( 组合根 ) 注入:
//
//	会话是进程级领域状态，不归机器人私有，多入口共享同一管理器，
//	保证同一 chatID 在任何入口都命中同一个会话。
func NewFeishuBot(appID, appSecret, workspace string, eng *engine.AgentEngine, sessions *engine.SessionManager) (*FeishuBot, error) {
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("appID or appSecret is empty")
	}
	if eng == nil {
		return nil, fmt.Errorf("agent engine is required")
	}
	if sessions == nil {
		return nil, fmt.Errorf("session manager is required")
	}

	const defaultAgentRunTimeout = 10 * time.Minute
	const defaultMessageSendTimeout = 10 * time.Second

	return &FeishuBot{
		appID:     appID,
		appSecret: appSecret,
		workspace: workspace,

		planMode:           true,
		agentRunTimeout:    defaultAgentRunTimeout,
		messageSendTimeout: defaultMessageSendTimeout,

		client:   lark.NewClient(appID, appSecret),
		engine:   eng,
		sessions: sessions,
	}, nil
}

func (b *FeishuBot) StartWithWebSocket(ctx context.Context, eventEncryptKey, verificationToken string) error {
	if eventEncryptKey == "" || verificationToken == "" {
		return fmt.Errorf("eventEncryptKey or verificationToken is empty")
	}

	slog.Info("[feishu] 正在以 WebSocket 模式启动飞书机器人客户端...")

	eventDispatcher := dispatcher.NewEventDispatcher(verificationToken, eventEncryptKey).
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
			chatID := *event.Event.Message.ChatId
			content := *event.Event.Message.Content

			var m map[string]string
			if err := json.Unmarshal([]byte(content), &m); err != nil {
				slog.Error("[feishu] 解析飞书消息内容失败", "error", err)
				return err
			}

			text, ok := m["text"]
			slog.Debug("[feishu] 收到飞书消息", "chat_id", chatID, "text", text)

			if ok && text != "" {
				// 调用引擎处理消息。
				go b.agentRun(ctx, chatID, text)
			}

			return nil
		}).
		OnP2MessageReadV1(func(_ context.Context, _ *larkim.P2MessageReadV1) error {
			// 忽略消息已读事件。
			return nil
		})

	wsClient := ws.NewClient(
		b.appID,
		b.appSecret,
		ws.WithEventHandler(eventDispatcher),
		ws.WithLogLevel(larkcore.LogLevelInfo),
		ws.WithAutoReconnect(true),
	)

	if err := wsClient.Start(ctx); err != nil {
		slog.Error("[feishu] 飞书客户端启动失败", "error", err)
		return err
	}

	slog.Info("[feishu] 飞书客户端已启动")
	return nil
}

func (b *FeishuBot) agentRun(ctx context.Context, chatID, prompt string) {
	reporter := NewFeishuReporter(b.client, chatID)

	// 飞书会话 ( chatID ) 与 Agent 会话一一对应，跨消息累积上下文。
	sess := b.sessions.Get(chatID, b.workspace, b.planMode)
	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: prompt,
	})

	agentRunCtx, agentRunCancel := context.WithTimeout(ctx, b.agentRunTimeout)
	defer agentRunCancel()

	if err := b.engine.Run(agentRunCtx, sess, reporter); err != nil {
		notifyCtx, notifyCancel := context.WithTimeout(ctx, b.messageSendTimeout)
		defer notifyCancel()

		if errors.Is(err, context.DeadlineExceeded) {
			_ = reporter.sendMessage(notifyCtx, fmt.Sprintf("引擎运行超时[%s]，任务已取消", b.agentRunTimeout))
			return
		}
		_ = reporter.sendMessage(notifyCtx, fmt.Sprintf("引擎运行失败: %v", err))
	}
}

var _ engine.Reporter = (*FeishuReporter)(nil)

// FeishuReporter 用于将引擎输出格式化后发给飞书。
type FeishuReporter struct {
	client *lark.Client
	chatID string
}

func NewFeishuReporter(client *lark.Client, chatID string) *FeishuReporter {
	return &FeishuReporter{
		client: client,
		chatID: chatID,
	}
}

func (r *FeishuReporter) OnThinking(ctx context.Context) error {
	return r.sendMessage(ctx, "模型正在慢思考中...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName, args string) error {
	return r.sendMessage(ctx, fmt.Sprintf("模型正在执行工具调用: %s, 参数: %s", toolName, args))
}

func (r *FeishuReporter) OnToolCallResult(ctx context.Context, toolName, result string, isError bool) error {
	if isError {
		return r.sendMessage(ctx, fmt.Sprintf("工具 [%s] 调用失败, 错误信息: %s", toolName, result))
	}
	return r.sendMessage(ctx, fmt.Sprintf("工具 [%s] 调用成功，返回结果: %s", toolName, result))
}

func (r *FeishuReporter) OnMessage(ctx context.Context, content string) error {
	return r.sendMessage(ctx, content)
}

func (r *FeishuReporter) sendMessage(ctx context.Context, text string) error {
	msg := map[string]string{
		"text": text,
	}

	bytes, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("json marshal failed: %w", err)
	}

	content := string(bytes)

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(r.chatID).
				MsgType(larkim.MsgTypeText).
				Content(content).
				Build(),
		).Build()

	if _, err = r.client.Im.Message.Create(ctx, req); err != nil {
		slog.Error("[feishu] 发送消息失败", "error", err)
		return err
	}
	return nil
}
