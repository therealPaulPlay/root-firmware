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
	connMu                sync.Mutex // Protects conn access
	running               bool
	stopChan              chan struct{}
	handlers              map[string]func(Message)
	rateLimitMessageCount int
	rateLimitLastReset    time.Time
	rateLimitMu           sync.Mutex
	sendChan              chan Message // Async send queue
	sendWg                sync.WaitGroup
}

var instance *RelayComm
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &RelayComm{
			handlers:           make(map[string]func(Message)),
			rateLimitLastReset: time.Now(),
			sendChan:           make(chan Message, 100), // Buffer 100 messages (~20 seconds at 5 msg/sec)
		}
		// Start async sender goroutine
		instance.sendWg.Add(1)
		go instance.sendLoop()
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
	r.connMu.Lock()
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.connMu.Unlock()

	// Close send channel and wait for sender to finish
	close(r.sendChan)
	r.sendWg.Wait()
}

// Send queues a message to be sent asynchronously
func (r *RelayComm) Send(msg Message) error {
	select {
	case r.sendChan <- msg:
		return nil
	default:
		return fmt.Errorf("send queue full, message dropped")
	}
}

// sendLoop processes messages from the send queue and writes them to the WebSocket
func (r *RelayComm) sendLoop() {
	defer r.sendWg.Done()

	for msg := range r.sendChan {
		r.connMu.Lock()
		conn := r.conn
		r.connMu.Unlock()

		if conn == nil {
			continue // Drop message if not connected
		}

		// Set write deadline
		if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			log.Printf("RelayComm: Failed to set write deadline: %v", err)
			continue
		}

		// Write message
		if err := conn.WriteJSON(msg); err != nil {
			log.Printf("RelayComm: Failed to send message: %v", err)
		}

		// Clear deadline
		_ = conn.SetWriteDeadline(time.Time{})
	}
}

// drainSendQueue discards all queued messages
func (r *RelayComm) drainSendQueue() {
	for {
		select {
		case <-r.sendChan:
			// Discard message
		default:
			return
		}
	}
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

			// Loop for handle messages (until connection closes)
			r.handleMessages()

			// Connection died - clear it and drain send queue
			r.connMu.Lock()
			if r.conn != nil {
				r.conn.Close()
				r.conn = nil
			}
			r.connMu.Unlock()

			// Drain send queue
			r.drainSendQueue()

			// Wait before reconnecting
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

	r.connMu.Lock()
	r.conn = conn
	r.connMu.Unlock()
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
