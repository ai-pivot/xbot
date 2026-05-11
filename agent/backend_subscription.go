package agent

import "xbot/protocol"

// SubscriptionManagement groups methods for LLM subscription CRUD.
type SubscriptionManagement interface {
	ListSubscriptions(senderID string) ([]protocol.Subscription, error)
	GetDefaultSubscription(senderID string) (*protocol.Subscription, error)
	AddSubscription(senderID string, sub protocol.Subscription) error
	RemoveSubscription(id string) error
	SetDefaultSubscription(id string, chatID string) error
	RenameSubscription(id, name string) error
	UpdateSubscription(id string, sub protocol.Subscription) error
	SetSubscriptionModel(id, model string) error
}
