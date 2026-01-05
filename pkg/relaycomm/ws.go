package relaycomm

import (
	"fmt"
	"log"
	"sync"
	"time"

	"root-firmware/pkg/config"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay       = 5 * time.Second
	maxMessagesPerSecond = 15
)

type Message struct {
	Type      string `json:"type"`
	Target    string `json:"target,omitempty"`    // "product" or "device"
	ProductID string `json:"productId,omitempty"` // Target/source product ID
	DeviceID  string `json:"deviceId,omitempty"`  // Target/source device ID
	RequestID string `json:"requestId,omitempty"` // Request tracking ID
	Payload   string `json:"payload,omitempty"`   // Encrypted (base64) or unencrypted JSON
}

type RelayComm struct {
	conn                  *websocket.Conn
	connMu                sync.Mutex // Protects WebSocket writes
	running               bool
	stopChan              chan struct{}
	handlers              map[string]func(Message)
	rateLimitMessageCount int
	rateLimitLastReset    time.Time
	rateLimitMu           sync.Mutex
}

var instance *RelayComm
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &RelayComm{
			handlers:           make(map[string]func(Message)),
			rateLimitLastReset: time.Now(),
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
// If already running, stops and restarts with the current relay domain
func (r *RelayComm) Start() error {
	// Stop existing connection if running
	if r.running {
		r.Stop()
	}

	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok {
		return fmt.Errorf("relay domain not configured")
	}

	// Reset rate limit counter
	r.rateLimitMu.Lock()
	r.rateLimitMessageCount = 0
	r.rateLimitLastReset = time.Now()
	r.rateLimitMu.Unlock()

	r.running = true
	r.stopChan = make(chan struct{})

	go r.connectLoop(relayDomain.(string))
	return nil
}

// Stop stops the relay connection
func (r *RelayComm) Stop() {
	if !r.running {
		return
	}

	r.running = false
	close(r.stopChan)

	// Close connection if exists
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
}

// Send sends a message to the relay server
func (r *RelayComm) Send(msg Message) error {
	r.connMu.Lock()
	defer r.connMu.Unlock()

	if r.conn == nil {
		return fmt.Errorf("not connected")
	}

	// Set write deadline
	if err := r.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}

	// Write
	err := r.conn.WriteJSON(msg)

	// Clear deadline
	_ = r.conn.SetWriteDeadline(time.Time{})

	return err
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

		// Check rate limit
		if !r.checkRateLimit() {
			log.Printf("RelayComm: Rate limit exceeded (%d messages/second), message type: %s", r.rateLimitMessageCount, msg.Type)
			continue
		}

		// Look up and call handler
		if handler, ok := r.handlers[msg.Type]; ok {
			go handler(msg)
		}
	}
}

// checkRateLimit returns false if rate limit exceeded
func (r *RelayComm) checkRateLimit() bool {
	r.rateLimitMu.Lock()
	defer r.rateLimitMu.Unlock()

	// Reset counter every second
	if time.Since(r.rateLimitLastReset) >= time.Second {
		r.rateLimitMessageCount = 0
		r.rateLimitLastReset = time.Now()
	}

	if r.rateLimitMessageCount >= maxMessagesPerSecond {
		return false
	}

	r.rateLimitMessageCount++
	return true
}
