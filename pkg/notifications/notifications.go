package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fxamacker/cbor/v2"
	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
)

const (
	MaxNotificationDevices = 5
	sendEndpoint           = "/notifications/send"
	uploadPreviewEndpoint  = "/notifications/upload-preview"
	httpTimeout            = 10 * time.Second
)

// NotificationDevice represents a device registered to receive push notifications
type NotificationDevice struct {
	DeviceID string `cbor:"deviceId"`
	FCMToken string `cbor:"fcmToken"`
}

type pendingNotification struct {
	title, eventID string
	preview        []byte
	sending        bool
}

type Notifications struct {
	mu             sync.Mutex
	lastNotifiedAt time.Time
	pending        map[string]*pendingNotification // Most recent undelivered notification per device
}

var instance *Notifications
var once sync.Once
var httpClient = &http.Client{Timeout: httpTimeout}

func Init() {
	once.Do(func() {
		instance = &Notifications{pending: make(map[string]*pendingNotification)}
	})
}

// ResetForTesting resets the singleton for test isolation
func ResetForTesting() {
	instance = nil
	once = sync.Once{}
}

func Get() *Notifications {
	if instance == nil {
		panic("notifications not initialized - call Init() first")
	}
	return instance
}

// Enable registers a device for push notifications
// The device must be paired. If an entry already exists for this device, it is overwritten
func (n *Notifications) Enable(deviceID, fcmToken string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Verify device is paired
	if _, ok := devices.Get().GetByID(deviceID); !ok {
		return fmt.Errorf("device not paired: %s", deviceID)
	}

	// Remove existing entry for this device (will be replaced)
	filtered := n.entriesWithout(deviceID)

	// Enforce max devices limit
	if len(filtered) >= MaxNotificationDevices {
		return fmt.Errorf("maximum of %d notification devices reached", MaxNotificationDevices)
	}

	filtered = append(filtered, NotificationDevice{DeviceID: deviceID, FCMToken: fcmToken})

	return config.Get().SetKey("notificationDevices", filtered)
}

// Disable removes a device from push notifications
func (n *Notifications) Disable(deviceID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.pending, deviceID)
	return config.Get().SetKey("notificationDevices", n.entriesWithout(deviceID))
}

// IsEnabled returns whether a specific device has notifications enabled
func (n *Notifications) IsEnabled(deviceID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	for _, entry := range n.getEntries() {
		if entry.DeviceID == deviceID {
			return true
		}
	}
	return false
}

// GetCooldownMinutes returns the configured notification cooldown in minutes (0 = off)
func (n *Notifications) GetCooldownMinutes() int {
	val, _ := config.Get().GetKey("notificationCooldownMinutes")
	v, _ := val.(uint64)
	return int(v)
}

// SetCooldownMinutes sets the notification cooldown in minutes (0 = off, max 30)
func (n *Notifications) SetCooldownMinutes(minutes int) error {
	if minutes < 0 || minutes > 30 {
		return fmt.Errorf("cooldown must be between 0 and 30 minutes")
	}
	return config.Get().SetKey("notificationCooldownMinutes", minutes)
}

// SendEventToAll queues an event notification for all registered devices and sends it in the background
// If preview is non-nil, it is encrypted per-device and uploaded for rich notifications
func (n *Notifications) SendEventToAll(eventType string, eventID string, preview []byte) {
	if eventType == "" {
		log.Printf("Notifications: SendEventToAll called with empty event type")
		return
	}

	cooldown := n.GetCooldownMinutes() // Get before holding notifications lock as it holds another lock internally

	n.mu.Lock()
	if cooldown > 0 && !n.lastNotifiedAt.IsZero() && time.Since(n.lastNotifiedAt) < time.Duration(cooldown)*time.Minute {
		n.mu.Unlock()
		return
	}
	title := strings.ToUpper(eventType[:1]) + eventType[1:] + " detected"
	for _, entry := range n.getEntries() {
		// One per device so in-flight state isn't shared, replaces any older undelivered notification
		n.pending[entry.DeviceID] = &pendingNotification{title: title, eventID: eventID, preview: preview}
	}
	n.lastNotifiedAt = time.Now()
	n.mu.Unlock()

	n.SendPending()
}

// SendPending asynchronously delivers each device's pending notification (new events and ones that failed earlier)
func (n *Notifications) SendPending() {
	relayDomain, productID, err := n.getRelayAndProductID()
	if err != nil {
		log.Printf("Notifications: %v", err)
		return
	}

	productPrivateKey, err := config.Get().GetProductPrivateKeyP256()
	if err != nil {
		log.Printf("Notifications: Failed to get product private key: %v", err)
		return
	}

	for _, entry := range n.getEntries() {
		dev, paired := devices.Get().GetByID(entry.DeviceID)
		if !paired {
			continue
		}

		n.mu.Lock()
		p := n.pending[entry.DeviceID]
		if p == nil || p.sending { // Nothing pending or already in flight
			n.mu.Unlock()
			continue
		}
		p.sending = true
		n.mu.Unlock()

		go func() {
			var imageURL string
			if p.preview != nil {
				imageURL = n.uploadEncryptedPreview(relayDomain, productPrivateKey, dev.PublicKey, p.preview)
			}
			sent := n.sendNotification(relayDomain, entry.FCMToken, p.title, dev.ProductAlias, productID, p.eventID, imageURL)

			n.mu.Lock()
			defer n.mu.Unlock()
			p.sending = false // Unsent notifications stay pending for the next retry
			if sent && n.pending[entry.DeviceID] == p {
				delete(n.pending, entry.DeviceID)
			}
		}()
	}
}

func (n *Notifications) getRelayAndProductID() (string, string, error) {
	rd, _ := config.Get().GetKey("relayDomain")
	pid, _ := config.Get().GetKey("id")
	relayDomain, _ := rd.(string)
	productID, _ := pid.(string)
	if relayDomain == "" || productID == "" {
		return "", "", fmt.Errorf("relay domain or product ID not configured")
	}
	return relayDomain, productID, nil
}

func (n *Notifications) sendNotification(relayDomain, fcmToken, title, body, productID, eventID, imageURL string) bool {
	data := map[string]string{
		"productId": productID,
		"eventId":   eventID,
		"title":     title,
		"body":      body,
	}
	if imageURL != "" {
		data["imageUrl"] = imageURL
		data["imageEncrypted"] = "true"
	}

	// Data-only message: no top-level "notification" key so Android always
	// routes through our custom messaging service, even when the app is backgrounded
	message := map[string]any{
		"token": fcmToken,
		"data":  data,
		"android": map[string]any{
			"priority": "high",
		},
		"apns": map[string]any{
			"headers": map[string]string{"apns-priority": "10"},
			"payload": map[string]any{
				"aps": map[string]any{
					"alert": map[string]string{
						"title": title,
						"body":  body,
					},
					"sound":           "default",
					"mutable-content": 1,
				},
			},
		},
	}

	jsonBody, err := json.Marshal(map[string]any{"message": message})
	if err != nil {
		log.Printf("Notifications: Failed to marshal payload: %v", err)
		return false
	}

	resp, err := postToRelay(fmt.Sprintf("https://%s%s", relayDomain, sendEndpoint), "application/json", jsonBody)
	if err != nil {
		log.Printf("Notifications: Failed to send: %v", err)
		return false
	}
	resp.Body.Close()
	return true
}

// uploadEncryptedPreview encrypts the preview for a specific device and uploads it to the relay
// Returns the image URL, or empty string on failure
func (n *Notifications) uploadEncryptedPreview(relayDomain string, productPrivateKey, devicePublicKey, preview []byte) string {
	session, err := rootproto.DeriveSessionP256(productPrivateKey, devicePublicKey)
	if err != nil {
		log.Printf("Notifications: Failed to derive session: %v", err)
		return ""
	}

	encrypted, err := session.Encrypt(preview, nil)
	if err != nil {
		log.Printf("Notifications: Failed to encrypt preview: %v", err)
		return ""
	}

	resp, err := postToRelay(fmt.Sprintf("https://%s%s", relayDomain, uploadPreviewEndpoint), "application/octet-stream", encrypted)
	if err != nil {
		log.Printf("Notifications: Failed to upload preview: %v", err)
		return ""
	}
	defer resp.Body.Close()

	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("Notifications: Failed to parse upload response: %v", err)
		return ""
	}

	return result.URL
}

func postToRelay(url, contentType string, body []byte) (*http.Response, error) {
	resp, err := httpClient.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("relay returned status %d", resp.StatusCode) // Non-200 responses count as failures
	}
	return resp, nil
}

func (n *Notifications) entriesWithout(deviceID string) []NotificationDevice {
	filtered := []NotificationDevice{}
	for _, entry := range n.getEntries() {
		if entry.DeviceID != deviceID {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (n *Notifications) getEntries() []NotificationDevice {
	val, ok := config.Get().GetKey("notificationDevices")
	if !ok {
		return []NotificationDevice{}
	}

	var entries []NotificationDevice
	data, err := cbor.Marshal(val)
	if err == nil {
		err = cbor.Unmarshal(data, &entries)
	}
	if err != nil {
		log.Printf("Notifications: Failed to decode notification devices: %v", err)
		return []NotificationDevice{}
	}
	return entries
}
