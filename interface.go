package driver

import (
	"context"
)

type Message struct {
	ctx       context.Context
	Type      string
	Sender    string
	Group     string
	Content   string
	EventID   string
	MessageID string
}

func (m *Message) GetContext() context.Context {
	return m.ctx
}

func (m *Message) SetContext(ctx context.Context) {
	m.ctx = ctx
}

type Receiver interface {
	Connect() error
	ReceiveMessages() <-chan *Message
}

type Sender interface {
	SendMessage(*Message) error
}

const (
	MessageTypeGroup   = "group"
	MessageTypePrivate = "private"
)
