package relaycomm

import (
	"fmt"
	"sync"
	"time"

	"root-firmware/pkg/config"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 5 * time.Second
)

type Message struct {
	Type             string `json:"type"`
	Target           string `json:"target,omitempty"`           // "product" or "device"
	ProductID        string `json:"productId,omitempty"`        // Target/source product ID
	DeviceID         string `json:"deviceId,omitempty"`         // Target/source device ID
	EncryptedPayload string `json:"encryptedPayload,omitempty"` // Base64 encrypted JSON
}

type RelayComm struct {
	conn     *websocket.Conn
	running  bool
	stopChan chan struct{}
	handlers map[string]func(Message)
}

var instance *RelayComm
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &RelayComm{
			handlers: make(map[string]func(Message)),
		}
	})
}

func Get() *RelayComm {
	if instance == nil {
		panic("relaycomm not initialized - call Init() first")
	}
	return instance
}

// On registers a handler for a message type
func (r *RelayComm) On(messageType string, handler func(Message)) {
	r.handlers[messageType] = handler
}

// Start connects to relay server and maintains connection
func (r *RelayComm) Start() error {
	if r.running {
		return fmt.Errorf("already running")
	}

	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		return fmt.Errorf("relay domain not configured")
	}

	r.running = true
	r.stopChan = make(chan struct{})

	go r.connectLoop(relayDomain.(string))
	return nil
}

// Send sends a message to the relay server
func (r *RelayComm) Send(msg Message) error {
	if r.conn == nil {
		return fmt.Errorf("not connected")
	}
	return r.conn.WriteJSON(msg)
}

func (r *RelayComm) connectLoop(relayDomain string) {
	for {
		select {
		case <-r.stopChan:
			return
		default:
			if err := r.connect(relayDomain); err != nil {
				time.Sleep(reconnectDelay)
				continue
			}

			// Handle messages until connection closes
			r.handleMessages()

			// Connection closed, reconnect
			time.Sleep(reconnectDelay)
		}
	}
}

func (r *RelayComm) connect(relayDomain string) error {
	// Get product ID for authentication
	id, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("product ID not found")
	}

	// Build WebSocket URL from domain
	url := fmt.Sprintf("wss://%s/ws?product-id=%s", relayDomain, id)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	r.conn = conn
	return nil
}

func (r *RelayComm) handleMessages() {
	for {
		var msg Message
		if err := r.conn.ReadJSON(&msg); err != nil {
			return
		}

		// Look up and call handler
		if handler, ok := r.handlers[msg.Type]; ok {
			go handler(msg)
		}
	}
}
