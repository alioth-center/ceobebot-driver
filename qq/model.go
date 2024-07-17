package qq

import (
	"encoding/json"
	"sync"
)

type Secret struct {
	Environment string `yaml:"environment"`
	BotID       string `yaml:"bot_id"`
	BotToken    string `yaml:"bot_token"`
	BotSecret   string `yaml:"bot_secret"`
}

type ClientOption struct {
	WebsocketURL string
	BotID        string
	BotToken     string
	BotSecret    string
	AccessToken  string
	Environment  string

	mtx sync.RWMutex
}

type GetAccessTokenRequest struct {
	AppID        string `json:"app_id"`
	ClientSecret string `json:"client_secret"`
}

type GetAccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   string `json:"expires_in"`
}

type GetWebsocketURLResponse struct {
	URL               string                              `json:"url"`
	Shards            int                                 `json:"shards"`
	SessionStartLimit GetWebsocketURLResSessionStartLimit `json:"session_start_limit"`
}

type GetWebsocketURLResSessionStartLimit struct {
	Total          int `json:"total"`
	Remaining      int `json:"remaining"`
	ResetAfter     int `json:"reset_after"`
	MaxConcurrency int `json:"max_concurrency"`
}

// RawWebsocketMessage 定义了 WebSocket 消息的结构体，用于解析接收到的消息
//
// 参考： [使用 websocket 接入]
//
// [使用 websocket 接入]: https://bot.q.qq.com/wiki/develop/api/gateway/reference.html#payload
type RawWebsocketMessage struct {
	OperationCode int             `json:"op"`
	Sequence      int             `json:"s"`
	EventType     string          `json:"t"`
	EventID       string          `json:"id"`
	MessageData   json.RawMessage `json:"d"`
}

type WsReadyMessage struct {
	Version   int                `json:"version"`
	SessionID string             `json:"session_id"`
	User      WsReadyMessageUser `json:"user"`
	Shard     [2]int             `json:"shard"`
}

type WsReadyMessageUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

type WsChatMessage struct {
	ID          string                `json:"id"`
	Timestamp   string                `json:"timestamp"`
	Content     string                `json:"content"`
	GroupOpenID string                `json:"group_openid"`
	Attachments []WsMessageAttachment `json:"attachments"`
	Author      WsMessageAuthor       `json:"author"`
}

type WsMessageAttachment struct {
	Url         string `json:"url"`
	Size        int    `json:"size"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

type WsMessageAuthor struct {
	MemberOpenID string `json:"member_openid,omitempty"`
	UserOpenID   string `json:"user_openid,omitempty"`
}

type RawWebsocketRequest struct {
	OperationCode int             `json:"op"`
	RequestData   json.RawMessage `json:"d"`
}

type WsAuthorizeRequest struct {
	Token      string         `json:"token"`
	Intents    int            `json:"intents"`
	Shard      [2]int         `json:"shard"`
	Properties map[string]any `json:"properties,omitempty"`
}

type WsResumeRequest struct {
	Token     string `json:"token"`
	SessionID string `json:"session_id"`
	Sequence  int    `json:"seq"`
}

type SendMessageRequest struct {
	Content     string `json:"content"`
	MessageType int    `json:"msg_type"`
	MessageID   string `json:"msg_id"`
	EventID     string `json:"event_id"`
}

type SendMessageResponse struct {
	ErrorCode int    `json:"err_code"`
	Message   string `json:"message"`
}

// OperationCodes 定义了 WebSocket 消息的操作码
//
// 参考： [opcode]
//
// [opcode]: https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/opcode.html
const (
	OperationCodeDispatch        = 0  // 服务端进行消息推送
	OperationCodeHeartbeat       = 1  // 客户端或服务端发送心跳
	OperationCodeIdentify        = 2  // 客户端发送鉴权
	OperationCodeResume          = 6  // 客户端恢复连接
	OperationCodeReconnect       = 7  // 服务端通知客户端重新连接
	OperationCodeInvalidSession  = 9  // 当 identify 或 resume 的时候，如果参数有错，服务端会返回该消息
	OperationCodeHello           = 10 // 当客户端与网关建立 ws 连接之后，就会收到该消息
	OperationCodeHeartbeatAck    = 11 // 当心跳发送成功之后，就会收到好消息
	OperationCodeHttpCallbackAck = 12 // 仅用于 http 回调模式的回包，代表机器人收到了平台推送的数据
)

const (
	EventTypeReady                = "READY"                   // 鉴权成功，查看 https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html
	EventTypeResumed              = "RESUMED"                 // 重连成功，查看 https://bot.q.qq.com/wiki/develop/api/gateway/reference.html
	EventTypeGroupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE" // 群组消息中有人 @ 机器人，查看 https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/send-receive/event.html#%E7%BE%A4%E8%81%8A-%E6%9C%BA%E5%99%A8%E4%BA%BA
	EventTypeC2CMessageCreate     = "C2C_MESSAGE_CREATE"      // 单聊消息，查看 https://bot.q.qq.com/wiki/develop/api-v2/server-inter/message/send-receive/event.html#%E5%8D%95%E8%81%8A%E6%B6%88%E6%81%AF
)

type EventShard int

const (
	EventShardGuild                    EventShard = 1 << 0
	EventShardMembers                  EventShard = 1 << 1
	EventShardGuildMessages            EventShard = 1 << 9
	EventShardGuildMessageReactions    EventShard = 1 << 10
	EventShardDirectMessage            EventShard = 1 << 12
	EventShardOpenForumsEvent          EventShard = 1 << 18
	EventShardAudioOrLiveChannelMember EventShard = 1 << 19
	EventShardInteraction              EventShard = 1 << 26
	EventShardMessageAudit             EventShard = 1 << 27
	EventShardForumsEvent              EventShard = 1 << 28
	EventShardAudioAction              EventShard = 1 << 29
	EventShardPublicGuildMessages      EventShard = 1 << 30
	EventShardQQMessages               EventShard = 1 << 25

	EventShardAll = EventShardGuild | EventShardMembers | EventShardGuildMessages |
		EventShardGuildMessageReactions | EventShardDirectMessage | EventShardOpenForumsEvent |
		EventShardAudioOrLiveChannelMember | EventShardInteraction | EventShardMessageAudit |
		EventShardForumsEvent | EventShardAudioAction | EventShardPublicGuildMessages | EventShardQQMessages
)

func BuildIntents(shards ...EventShard) int {
	var intent int
	for _, shard := range shards {
		intent |= int(shard)
	}

	return intent
}

const (
	MessageTypeText = 0
)
