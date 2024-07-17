package main

import (
	"github.com/alioth-center/ceobebot-driver/qq"
	"github.com/alioth-center/infrastructure/config"
)

func main() {
	secret := qq.Secret{}
	loadErr := config.LoadConfig(&secret, "./config.yaml")
	if loadErr != nil {
		panic(loadErr)
	}

	client := qq.NewClient(secret)

	client.ServeAsync()

	for msg := range client.ReceiveMessages() {
		msg.Content = "你发送的内容是: " + msg.Content
		_ = client.SendMessage(msg)
	}
}
