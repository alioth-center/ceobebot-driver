package qq

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alioth-center/ceobebot-driver"
	"github.com/alioth-center/infrastructure/logger"
	"github.com/alioth-center/infrastructure/network/http"
	"github.com/alioth-center/infrastructure/trace"
	"github.com/alioth-center/infrastructure/utils/values"
	"github.com/gorilla/websocket"
)

type Client struct {
	log  logger.Logger
	opt  ClientOption
	conn *websocket.Conn
	cli  http.Client

	msgChan         chan *driver.Message
	sequence        atomic.Int64
	session         atomic.Value
	senderAvailable atomic.Bool

	waitConn      chan struct{}
	authSucc      chan struct{}
	hbTicker      <-chan time.Time
	hbSucc        chan struct{}
	refreshTicker chan struct{}
}

func NewClient(secret Secret) *Client {
	log := logger.Default()
	client := &Client{
		log: log,
		cli: http.NewLoggerClient(log),

		opt: ClientOption{
			WebsocketURL: "",
			BotID:        secret.BotID,
			BotToken:     secret.BotToken,
			BotSecret:    secret.BotSecret,
			Environment:  secret.Environment,
			mtx:          sync.RWMutex{},
		},
		sequence:        atomic.Int64{},
		session:         atomic.Value{},
		senderAvailable: atomic.Bool{},

		msgChan:       make(chan *driver.Message, 512),
		waitConn:      make(chan struct{}, 1),
		authSucc:      make(chan struct{}, 1),
		hbTicker:      time.NewTicker(time.Second * 30).C,
		hbSucc:        make(chan struct{}, 1),
		refreshTicker: make(chan struct{}, 1),
	}

	return client
}

func (r *Client) ServeAsync() {
	// 维护 Token
	go r.maintainToken()

	// 等待 Token 获取成功
	time.Sleep(time.Second * 5)

	// 准备 Websocket URL
	r.prepareWebsocketURL()

	// 连接 Websocket
	if connErr := r.Connect(); connErr != nil {
		r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("connect websocket error").WithData(connErr))
		return
	}
}

func (r *Client) Connect() error {
	ctx := trace.NewContext()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// connect to websocket server
	r.opt.mtx.RLock()
	conn, _, dialErr := websocket.DefaultDialer.Dial(r.opt.WebsocketURL, nil)
	r.opt.mtx.RUnlock()
	if dialErr != nil {
		return fmt.Errorf("failed to dial websocket: %w", dialErr)
	}
	if conn == nil {
		return fmt.Errorf("connection is nil")
	}

	// 连接成功，开始监听消息
	r.conn = conn
	go r.listen()

	// 成功建立连接后，发送鉴权消息
	<-r.waitConn
	r.log.Info(logger.NewFields().WithBaseFields(baseLogField).WithMessage("websocket connected, start authorization"))
	authMsg := &WsAuthorizeRequest{
		Token:   r.buildWebsocketAuthToken(),
		Intents: int(EventShardAll),
		Shard:   [2]int{0, 1},
	}

	r.writeMessage(ctx, OperationCodeIdentify, authMsg)
	select {
	case <-r.authSucc:
		r.log.Info(logger.NewFields().WithBaseFields(baseLogField).WithMessage("authorization success, maintain heartbeat"))
		// 鉴权成功
		go r.heartbeat()
	case <-time.After(time.Second * 10):
		// 鉴权超时
		panic("authorization timeout")
	}

	return nil
}

func (r *Client) ReceiveMessages() <-chan *driver.Message {
	return r.msgChan
}

func (r *Client) SendMessage(msg *driver.Message) error {
	if r.senderAvailable.Load() {
		var (
			responseParser http.ResponseParser
			executeErr     error
		)

		switch msg.Type {
		case driver.MessageTypePrivate:
			responseParser, executeErr = r.cli.ExecuteRequest(r.prepareBaseRequest().
				WithMethod(http.POST).WithPathTemplate(r.buildEndpoint(SendPrivateMessageEndpoint), map[string]string{"user_openid": msg.Sender}).
				WithJsonBody(&SendMessageRequest{
					MessageType: MessageTypeText,
					Content:     msg.Content,
					MessageID:   msg.MessageID,
					EventID:     msg.EventID,
				}),
			)
			if executeErr != nil {
				return fmt.Errorf("send private message error: %w", executeErr)
			}
		case driver.MessageTypeGroup:
			responseParser, executeErr = r.cli.ExecuteRequest(r.prepareBaseRequest().
				WithMethod(http.POST).WithPathTemplate(r.buildEndpoint(SendGroupMessageEndpoint), map[string]string{"group_openid": msg.Group}).
				WithJsonBody(&SendMessageRequest{
					MessageType: MessageTypeText,
					Content:     msg.Content,
					MessageID:   msg.MessageID,
					EventID:     msg.EventID,
				}),
			)
			if executeErr != nil {
				return fmt.Errorf("send group message error: %w", executeErr)
			}
		}

		// 状态码异常，重试发送错误信息
		if !http.StatusCodeIs2XX(responseParser) {
			errMsg := &SendMessageResponse{}
			_ = responseParser.BindJson(errMsg)
			if errMsg.Message != "" {
				msg.Content = fmt.Sprintf("发送信息失败：(%d) %s", errMsg.ErrorCode, errMsg.Message)
				return r.SendMessage(msg)
			}
		}
	}

	return nil
}

func (r *Client) listen() {
	for {
		// any message type will be unmarshalled to a message struct
		_, message, readMessageErr := r.conn.ReadMessage()
		if readMessageErr != nil {
			// read message error, log and continue
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("websocket read message error").WithData(readMessageErr).WithField("message", string(message)))
			continue
		}

		// unmarshal message to standard websocket message struct
		request := &RawWebsocketMessage{}
		unmarshalErr := json.Unmarshal(message, &request)
		if unmarshalErr != nil {
			// unmarshal error, log and continue
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("unmarshal websocket message error").WithData(unmarshalErr).WithField("message", string(message)))
			continue
		}

		// 对于控制类消息，需要根据 OperationCode 进行处理
		switch request.OperationCode {
		case OperationCodeDispatch:
			// dispatch 消息，需要根据 EventType 进行处理
			r.handleMessage(request, request.EventType)
		case OperationCodeHello:
			// hello 消息，表示连接成功，可以进行下一步操作
			r.waitConn <- struct{}{}
		case OperationCodeHeartbeatAck:
			// 心跳发送成功，发送成功信号
			r.hbSucc <- struct{}{}
		}
	}
}

func (r *Client) heartbeat() {
	timer := time.NewTimer(time.Second * 10)

	index := uint(0)
	for range r.hbTicker {
		// 收到心跳信号，发送心跳
		ctx := trace.NewContext()
		sent := false

		r.writeMessage(ctx, OperationCodeHeartbeat, int(r.sequence.Load()))
		for i := 0; i < 12; i++ {
			select {
			case <-r.hbSucc:
				// 每10分钟记录一次心跳成功
				if index = index + 1; index%20 == 0 {
					r.log.Info(logger.NewFields().WithBaseFields(baseLogField).WithMessage("heartbeat success"))
				}
				// 心跳发送成功，退出循环
				sent = true
			case <-timer.C:
				// 心跳发送超时，重试心跳
				r.writeMessage(ctx, OperationCodeHeartbeat, int(r.sequence.Load()))
				// 重置定时器
				timer.Reset(time.Second * 10)
			}

			if sent {
				break
			}
		}

		if !sent {
			// 两分钟都没有收到心跳成功信号，尝试重连
			r.writeMessage(ctx, OperationCodeResume, &WsResumeRequest{
				Token:     r.buildWebsocketAuthToken(),
				SessionID: r.session.Load().(string),
				Sequence:  int(r.sequence.Load()),
			})
		}
	}

	timer.Stop()
}

func (r *Client) handleMessage(msg *RawWebsocketMessage, msgType string) {
	switch msgType {
	case EventTypeReady:
		readyMsg := &WsReadyMessage{}
		if unmarshalErr := json.Unmarshal(msg.MessageData, readyMsg); unmarshalErr != nil {
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("unmarshal ready message error").WithData(unmarshalErr).WithField("message", string(msg.MessageData)))
			return
		}

		// 成功鉴权，发送成功信号
		r.sequence.Store(int64(msg.Sequence))
		r.session.Store(readyMsg.SessionID)
		r.log.Info(logger.NewFields().WithBaseFields(baseLogField).WithTraceID(readyMsg.SessionID).WithMessage("authorization success").WithData(readyMsg.User))
		r.authSucc <- struct{}{}
	case EventTypeResumed:
		r.sequence.Store(int64(msg.Sequence))
		r.log.Info(logger.NewFields().WithBaseFields(baseLogField).WithMessage("reconnect success"))
	case EventTypeGroupAtMessageCreate:
		r.sequence.Store(int64(msg.Sequence))
		message := &WsChatMessage{}
		if unmarshalErr := json.Unmarshal(msg.MessageData, message); unmarshalErr != nil {
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("unmarshal group at message error").WithData(unmarshalErr).WithField("message", string(msg.MessageData)))
			return
		}

		dMsg := &driver.Message{
			Type:      driver.MessageTypeGroup,
			Sender:    message.Author.MemberOpenID,
			Group:     message.GroupOpenID,
			Content:   message.Content,
			EventID:   msg.EventID,
			MessageID: message.ID,
		}
		dMsg.SetContext(trace.NewContext())
		r.msgChan <- dMsg
	case EventTypeC2CMessageCreate:
		r.sequence.Store(int64(msg.Sequence))
		message := &WsChatMessage{}
		if unmarshalErr := json.Unmarshal(msg.MessageData, message); unmarshalErr != nil {
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("unmarshal c2c message error").WithData(unmarshalErr).WithField("message", string(msg.MessageData)))
			return
		}

		dMsg := &driver.Message{
			Type:      driver.MessageTypePrivate,
			Sender:    message.Author.UserOpenID,
			Content:   message.Content,
			EventID:   msg.EventID,
			MessageID: message.ID,
		}
		dMsg.SetContext(trace.NewContext())
		r.msgChan <- dMsg
	default:
		r.log.Warn(logger.NewFields().WithBaseFields(baseLogField).WithMessage("unsupported message type").WithData(string(msg.MessageData)).WithField("type", msgType))
	}
}

func (r *Client) writeMessage(ctx context.Context, opCode int, data any) {
	requestData, marshalErr := json.Marshal(data)
	if marshalErr != nil {
		r.log.Error(logger.NewFields(ctx).WithBaseFields(baseLogField).WithMessage("marshal websocket message error").WithData(marshalErr).WithField("data", data))
		return
	}

	sendErr := r.conn.WriteJSON(&RawWebsocketRequest{
		OperationCode: opCode,
		RequestData:   requestData,
	})
	if sendErr != nil {
		r.log.Error(logger.NewFields(ctx).WithBaseFields(baseLogField).WithMessage("send websocket message error").WithData(sendErr).WithField("data", data))
	}
}

// maintainToken 维护 Token 的刷新
//
// 沟槽的腾讯的机器人要有三个 token，脑子是乐开了洞
func (r *Client) maintainToken() {
	timer := time.NewTimer(0)

	for range timer.C {
		r.opt.mtx.RLock()
		responseParser, executeErr := r.cli.ExecuteRequest(http.NewRequestBuilder().
			WithMethod(http.POST).WithPath(AuthorizationEndpoint).
			WithJsonBody(&GetAccessTokenRequest{AppID: r.opt.BotID, ClientSecret: r.opt.BotSecret}))
		r.opt.mtx.RUnlock()
		if executeErr != nil {
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("get access token error").WithData(executeErr))
			return
		}

		response := &GetAccessTokenResponse{}
		if bindErr := responseParser.BindJson(response); bindErr != nil {
			r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("parse access token error").WithData(bindErr))
			return
		}

		r.opt.mtx.Lock()
		r.senderAvailable.Store(true)
		r.opt.AccessToken = response.AccessToken
		r.opt.mtx.Unlock()

		// 到期前一分钟才可以刷新 Token，脑子简直他妈有💩只给一分钟窗口期
		const refreshWindow = 50
		timer.Reset(time.Duration(values.StringToInt(response.ExpiresIn, 0)-refreshWindow) * time.Second)
	}

	timer.Stop()
}

func (r *Client) prepareWebsocketURL() {
	if !r.senderAvailable.Load() {
		r.log.Warn(logger.NewFields().WithBaseFields(baseLogField).WithMessage("client is not available"))
	}

	responseParser, executeErr := r.cli.ExecuteRequest(r.prepareBaseRequest().WithMethod(http.GET).WithPath(r.buildEndpoint(GetWebsocketURLEndpoint)))
	if executeErr != nil {
		r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("get websocket url error").WithData(executeErr))
		return
	}

	response := &GetWebsocketURLResponse{}
	if bindErr := responseParser.BindJson(response); bindErr != nil {
		r.log.Error(logger.NewFields().WithBaseFields(baseLogField).WithMessage("parse websocket url error").WithData(bindErr))
		return
	}

	r.opt.mtx.Lock()
	r.opt.WebsocketURL = response.URL
	r.opt.mtx.Unlock()
}

func (r *Client) prepareBaseRequest() http.RequestBuilder {
	r.opt.mtx.RLock()
	defer r.opt.mtx.RUnlock()
	return http.NewRequestBuilder().
		WithHeader("X-Union-Appid", r.opt.BotID).
		WithHeader("Authorization", values.BuildStrings("QQBot ", r.opt.AccessToken))
}

// GetContext 根据消息内容获取上下文
//
// Deprecated: 傻逼腾讯你妈了个臭逼，不会写文档能不能不写啊
func (r *Client) GetContext(msg *RawWebsocketMessage) context.Context {
	// 对于可以推定 SessionID 的消息，EventID 格式为 <EventType>:<SessionID>，取 SessionID 作为 TraceID
	slice := strings.Split(msg.EventID, ":")
	if len(slice) != 2 {
		// 无法推定 SessionID，使用默认 TraceID
		return trace.NewContext()
	}

	// 使用 SessionID 作为 TraceID
	return trace.NewContextWithTid(slice[1])
}

func (r *Client) buildWebsocketAuthToken() string {
	r.opt.mtx.RLock()
	defer r.opt.mtx.RUnlock()
	return values.BuildStrings(r.opt.BotID, ".", r.opt.BotToken)
}

func (r *Client) buildEndpoint(path string) string {
	r.opt.mtx.RLock()
	defer r.opt.mtx.RUnlock()
	switch r.opt.Environment {
	case "sandbox":
		return values.BuildStrings(SandboxEndpoint, path)
	case "live":
		return values.BuildStrings(LiveEndpoint, path)
	default:
		return values.BuildStrings(LiveEndpoint, path)
	}
}

func (r *Client) getRealEventID(eventID string) string {
	slice := strings.Split(eventID, ":")
	if len(slice) != 2 {
		return eventID
	}

	return slice[1]
}

const (
	LiveEndpoint          = "https://api.sgroup.qq.com"
	SandboxEndpoint       = "https://sandbox.api.sgroup.qq.com"
	AuthorizationEndpoint = "https://bots.qq.com/app/getAppAccessToken"

	GetWebsocketURLEndpoint    = "/gateway/bot"
	SendPrivateMessageEndpoint = "/v2/users/${user_openid}/messages"
	SendGroupMessageEndpoint   = "/v2/groups/${group_openid}/messages"
)

var baseLogField = logger.NewFields().WithService("ceobebot-driver").WithField("driver", "qq")
