package relaycomm

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"root-firmware/pkg/config"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 5 * time.Second
	pingInterval   = 30 * time.Second
)

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type RelayComm struct {
	conn     *websocket.Conn
	running  bool
	stopChan chan struct{}
	handlers map[string]func(json.RawMessage)
}

var instance *RelayComm
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &RelayComm{
			handlers: make(map[string]func(json.RawMessage)),
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
func (r *RelayComm) On(messageType string, handler func(json.RawMessage)) {
	r.handlers[messageType] = handler
}

// Start connects to relay server and maintains connection
func (r *RelayComm) Start() error {
	if r.running {
		return fmt.Errorf("already running")
	}

	relayURL, ok := config.Get().GetKey("relayUrl")
	if !ok {
		return fmt.Errorf("relay URL not configured")
	}

	r.running = true
	r.stopChan = make(chan struct{})

	go r.connectLoop(relayURL.(string))
	return nil
}

// Send sends a message to the relay server
func (r *RelayComm) Send(messageType string, payload interface{}) error {
	if r.conn == nil {
		return fmt.Errorf("not connected")
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	msg := Message{
		Type:    messageType,
		Payload: payloadJSON,
	}

	return r.conn.WriteJSON(msg)
}

func (r *RelayComm) connectLoop(relayURL string) {
	for {
		select {
		case <-r.stopChan:
			return
		default:
			if err := r.connect(relayURL); err != nil {
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

func (r *RelayComm) connect(relayURL string) error {
	// Get camera ID for authentication
	id, ok := config.Get().GetKey("id")
	if !ok {
		return fmt.Errorf("camera ID not found")
	}

	// Connect to relay with camera ID in query params
	url := fmt.Sprintf("%s?cameraId=%s", relayURL, id)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}

	r.conn = conn

	// Start ping loop
	go r.pingLoop()

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
			go handler(msg.Payload)
		}
	}
}

func (r *RelayComm) pingLoop() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			return
		case <-ticker.C:
			if r.conn != nil {
				r.conn.WriteMessage(websocket.PingMessage, nil)
			}
		}
	}
}
