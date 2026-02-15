package relaycomm

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/updater"

	"github.com/fxamacker/cbor/v2"
	"github.com/gorilla/websocket"
)

const (
	initialReconnectDelay = time.Second
	maxReconnectDelay     = 10 * time.Second
	dialTimeout           = 8 * time.Second
	maxMessagesPerSecond  = 25
)

type Message struct {
	Type      string `cbor:"type"`
	OriginID  string `cbor:"originId"`
	TargetID  string `cbor:"targetId"`
	RequestID string `cbor:"requestId"`
	Payload   []byte `cbor:"payload,omitempty"`
}

type RelayComm struct {
	handlers  map[string]func(Message)
	sendChan  chan Message
	stopChan  chan struct{}
	doneChan  chan struct{}
	rateMu    sync.Mutex
	rateCount int
	rateReset time.Time
}

var instance *RelayComm
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &RelayComm{
			handlers:  make(map[string]func(Message)),
			rateReset: time.Now(),
		}
		registerHandlers(instance)
	})
}

func Get() *RelayComm {
	if instance == nil {
		panic("relaycomm not initialized - call Init() first")
	}
	return instance
}

func (r *RelayComm) On(messageType string, handler func(Message)) {
	r.handlers[messageType] = handler
}

// Start connects to relay server. If already running, restarts with current config
// Does nothing if relay domain is not configured
func (r *RelayComm) Start() {
	r.Stop()

	relayDomain, ok := config.Get().GetKey("relayDomain")
	if !ok || relayDomain == "" {
		log.Println("RelayComm: Not starting, relay domain not configured")
		return
	}

	r.rateMu.Lock()
	r.rateCount = 0
	r.rateReset = time.Now()
	r.rateMu.Unlock()

	r.sendChan = make(chan Message, 50)
	r.stopChan = make(chan struct{})
	r.doneChan = make(chan struct{})

	go r.run(relayDomain.(string))
}

// Stop shuts down the relay connection and waits for cleanup
func (r *RelayComm) Stop() {
	if r.stopChan == nil {
		return
	}
	close(r.stopChan)
	<-r.doneChan
	r.stopChan = nil
	r.doneChan = nil
	r.sendChan = nil
}

// Send queues a message. Returns error if queue full or not connected
func (r *RelayComm) Send(msg Message) error {
	if r.sendChan == nil {
		return fmt.Errorf("not connected")
	}
	select {
	case r.sendChan <- msg:
		return nil
	default:
		return fmt.Errorf("send queue full")
	}
}

// run is the main loop - single goroutine that owns the connection
func (r *RelayComm) run(relayDomain string) {
	defer close(r.doneChan)

	delay := initialReconnectDelay
	for {
		select {
		case <-r.stopChan:
			return
		default:
		}

		conn := r.dial(relayDomain)
		if conn == nil {
			select {
			case <-r.stopChan:
				return
			case <-time.After(delay):
				delay = min(delay*3/2, maxReconnectDelay)
				continue
			}
		}

		delay = initialReconnectDelay
		updater.MarkRelayConnected()
		r.handleConnection(conn)
		conn.Close()

		select {
		case <-r.stopChan:
			return
		case <-time.After(initialReconnectDelay):
		}
	}
}

func (r *RelayComm) dial(relayDomain string) *websocket.Conn {
	id, ok := config.Get().GetKey("id")
	if !ok {
		return nil
	}

	url := fmt.Sprintf("wss://%s/ws?client-id=%s", relayDomain, id)
	dialer := websocket.Dialer{
		NetDial: func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, dialTimeout)
		},
		HandshakeTimeout: dialTimeout,
	}

	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		log.Printf("RelayComm: Dial failed: %v", err)
		return nil
	}
	return conn
}

// handleConnection manages a single connection - reads and writes until closed or stopped
func (r *RelayComm) handleConnection(conn *websocket.Conn) {
	readDone := make(chan struct{})

	// Reader goroutine
	go func() {
		defer close(readDone)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg Message
			if err := cbor.Unmarshal(data, &msg); err != nil {
				log.Printf("RelayComm: Failed to decode message: %v", err)
				continue
			}
			if r.checkRateLimit() {
				if handler, ok := r.handlers[msg.Type]; ok {
					go handler(msg)
				}
			}
		}
	}()

	// Main loop handles sends and stop signal
	for {
		select {
		case <-r.stopChan:
			conn.Close() // Unblocks reader
			<-readDone
			return
		case <-readDone:
			return
		case msg := <-r.sendChan:
			data, err := cbor.Marshal(msg)
			if err != nil {
				log.Printf("RelayComm: Failed to encode message: %v", err)
				continue
			}
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				log.Printf("RelayComm: Send failed: %v", err)
			}
			conn.SetWriteDeadline(time.Time{})
		}
	}
}

func (r *RelayComm) checkRateLimit() bool {
	r.rateMu.Lock()
	defer r.rateMu.Unlock()

	if time.Since(r.rateReset) >= time.Second {
		r.rateCount = 0
		r.rateReset = time.Now()
	}
	if r.rateCount >= maxMessagesPerSecond {
		return false
	}
	r.rateCount++
	return true
}
