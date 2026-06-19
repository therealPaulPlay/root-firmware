package relaycomm

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/updater"
)

const (
	maxReconnectDelay = 10 * time.Second
	dialTimeout       = 8 * time.Second
)

type RelayComm struct {
	server          *rootproto.Server
	sendChan        chan []byte
	lowPrioSendChan chan []byte
	stopChan        chan struct{}
	doneChan        chan struct{}
}

var instance *RelayComm
var once sync.Once

func Init() error {
	var err error
	once.Do(func() {
		instance = &RelayComm{}
		// Product ID from config becomes the server's selfID on the wire
		productID, _ := config.Get().GetKey("id")
		productIDStr, _ := productID.(string)
		instance.server, err = rootproto.NewServer(productIDStr, rootproto.KeyStore{
			GetPrivateKey: func(keyType string) *rootproto.PrivateKey {
				switch keyType {
				case rootproto.KeyTypeP256:
					key, err := config.Get().GetProductPrivateKeyP256()
					if err != nil {
						return nil
					}
					return &rootproto.PrivateKey{Key: key, KeyType: rootproto.KeyTypeP256}
				default:
					return nil
				}
			},
			GetClientPublicKey: func(clientID string) *rootproto.PublicKey {
				device, ok := devices.Get().GetByID(clientID)
				if !ok {
					return nil
				}
				return &rootproto.PublicKey{Key: device.PublicKey, KeyType: device.PublicKeyType}
			},
			CommitClientPublicKey: func(clientID string, newPublicKey *rootproto.PublicKey) error {
				return devices.Get().RenewKey(clientID, newPublicKey.Key, newPublicKey.KeyType)
			},
		}, persistentReplayStore())
		if err != nil {
			return
		}
		registerHandlers(instance.server)
	})
	return err
}

func Get() *RelayComm {
	if instance == nil {
		panic("relaycomm not initialized - call Init() first")
	}
	return instance
}

// ClearClient drops all per-client state (cached session and replay history)
func (r *RelayComm) ClearClient(clientID string) error {
	return r.server.ClearClient(clientID)
}

// lowPrioWriteFn returns a WriteFn that routes outbound envelope bytes through the
// low-priority send channel, used for server-initiated pushes (stream/file chunks)
func lowPrioWriteFn() rootproto.WriteFn {
	return func(bytes []byte) error {
		return Get().enqueueLowPrio(bytes)
	}
}

func (r *RelayComm) enqueueHighPrio(bytes []byte) error {
	if r.sendChan == nil {
		return fmt.Errorf("not connected")
	}
	select {
	case r.sendChan <- bytes:
		return nil
	default:
		return fmt.Errorf("send queue full")
	}
}

func (r *RelayComm) enqueueLowPrio(bytes []byte) error {
	if r.lowPrioSendChan == nil {
		return fmt.Errorf("not connected")
	}
	select {
	case r.lowPrioSendChan <- bytes:
		return nil
	default:
		return fmt.Errorf("send queue full")
	}
}

// Start connects to relay server. If already running, restarts with current config
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

	r.sendChan = make(chan []byte, 25) // Keep buffer low, don't overwhelm WS
	r.lowPrioSendChan = make(chan []byte, 25)
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
		// Drain pending writes to avoid sending old bytes (potentially with old keys) on the new connection
		for len(r.sendChan) > 0 {
			<-r.sendChan
		}
		for len(r.lowPrioSendChan) > 0 {
			<-r.lowPrioSendChan
		}
		updater.MarkRelayConnected()
		markInitialRelayConnect()
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

func (r *RelayComm) handleConnection(conn *websocket.Conn) {
	readDone := make(chan struct{})

	// WriteFn for request replies (high-priority path)
	replyWriteFn := func(bytes []byte) error {
		return r.enqueueHighPrio(bytes)
	}

	// Reader goroutine
	go func() {
		defer close(readDone)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := r.server.Receive(data, replyWriteFn); err != nil {
				log.Printf("RelayComm: Receive failed: %v", err)
			}
		}
	}()

	// Write helper — on failure, close conn and wait for reader to exit
	write := func(bytes []byte) bool {
		if r.writeBytes(conn, bytes) != nil {
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
		case bytes := <-r.sendChan:
			if !write(bytes) {
				return
			}
		case bytes := <-r.lowPrioSendChan:
			// Drain any pending high-priority message first
			select {
			case hi := <-r.sendChan:
				if !write(hi) {
					return
				}
			default:
			}
			if !write(bytes) {
				return
			}
		}
	}
}

// markInitialRelayConnect persists that the relay has been reached at least once
func markInitialRelayConnect() {
	if val, ok := config.Get().GetKey("initialRelayConnect"); ok {
		if b, ok := val.(bool); ok && b {
			return
		}
	}
	log.Println("RelayComm: Marked initial relay connection as established")
	config.Get().SetKey("initialRelayConnect", true)
}

func (r *RelayComm) writeBytes(conn *websocket.Conn, data []byte) error {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		log.Printf("RelayComm: Send failed: %v", err)
		return err
	}
	return nil
}
