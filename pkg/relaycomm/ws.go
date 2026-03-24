package relaycomm

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"root-firmware/pkg/config"
	"root-firmware/pkg/updater"

	"github.com/fxamacker/cbor/v2"
	"github.com/gorilla/websocket"
)

const (
	maxReconnectDelay    = 10 * time.Second
	dialTimeout          = 8 * time.Second
	maxMessagesPerSecond = 25
)

type Message struct {
	Type      string `cbor:"type"`
	OriginID  string `cbor:"originId"`
	TargetID  string `cbor:"targetId"`
	RequestID string `cbor:"requestId"`
	Payload   []byte `cbor:"payload,omitempty"`
}

type RelayComm struct {
	handlers        map[string]func(Message)
	sendChan        chan Message // Most messages (prioritized)
	lowPrioSendChan chan Message // Low priority data (stream, recordings..) — only sent when sendChan is empty (lower priority)
	stopChan        chan struct{}
	doneChan        chan struct{}
	rateMu          sync.Mutex
	rateCount       int
	rateReset       time.Time
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
	if !ok {
		log.Println("RelayComm: Not starting, relay domain not configured")
		return
	}
	relayDomainStr, ok := relayDomain.(string)
	if !ok {
		log.Println("RelayComm: Not starting, relay domain has invalid type")
		return
	}

	r.rateMu.Lock()
	r.rateCount = 0
	r.rateReset = time.Now()
	r.rateMu.Unlock()

	r.sendChan = make(chan Message, 25) // Keep buffer low, don't overwhelm WS
	r.lowPrioSendChan = make(chan Message, 25)
	r.stopChan = make(chan struct{})
	r.doneChan = make(chan struct{})

	go r.run(relayDomainStr)
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
	r.lowPrioSendChan = nil
}

// Send queues a message, routing stream chunks to a lower-priority channel
func (r *RelayComm) Send(msg Message) error {
	ch := r.sendChan
	if strings.HasPrefix(msg.Type, MsgStreamVideoChunk) || strings.HasPrefix(msg.Type, MsgStreamAudioChunk) || strings.HasPrefix(msg.Type, MsgGetRecording) {
		ch = r.lowPrioSendChan
	}
	if ch == nil {
		return fmt.Errorf("not connected")
	}
	select {
	case ch <- msg:
		return nil
	default:
		return fmt.Errorf("send queue full")
	}
}

// run is the main loop - single goroutine that owns the connection
func (r *RelayComm) run(relayDomain string) {
	defer close(r.doneChan)

	delay := time.Second
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

		delay = time.Second
		updater.MarkRelayConnected()
		r.handleConnection(conn)
		conn.Close()

		select {
		case <-r.stopChan:
			return
		case <-time.After(time.Second):
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

	// Write helper — on failure, close conn and wait for reader to exit
	write := func(msg Message) bool {
		if r.writeMsg(conn, msg) != nil {
			conn.Close()
			<-readDone
			return false
		}
		return true
	}

	// Main loop: always drain sendChan (high priority) before lowPrioSendChan
	for {
		select {
		case <-r.stopChan:
			conn.Close()
			<-readDone
			return
		case <-readDone:
			return
		case msg := <-r.sendChan:
			if !write(msg) {
				return
			}
		case msg := <-r.lowPrioSendChan:
			// Drain any pending high-priority message first
			select {
			case hi := <-r.sendChan:
				if !write(hi) {
					return
				}
			default:
			}
			if !write(msg) {
				return
			}
		}
	}
}

func (r *RelayComm) writeMsg(conn *websocket.Conn, msg Message) error {
	data, err := cbor.Marshal(msg)
	if err != nil {
		log.Printf("RelayComm: Failed to encode message: %v", err)
		return nil // Don't kill connection for marshal errors
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		log.Printf("RelayComm: Send failed: %v", err)
		return err
	}
	return nil
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
