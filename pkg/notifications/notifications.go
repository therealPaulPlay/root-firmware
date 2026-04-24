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
	"github.com/therealPaulPlay/root-e2ee-protocol/go-server"

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

type Notifications struct {
	mu             sync.Mutex
	lastNotifiedAt time.Time
}

var instance *Notifications
var once sync.Once

func Init() {
	once.Do(func() {
		instance = &Notifications{}
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

	entries := n.getEntries()

	// Remove existing entry for this device (will be replaced)
	filtered := make([]NotificationDevice, 0, len(entries))
	for _, entry := range entries {
		if entry.DeviceID != deviceID {
			filtered = append(filtered, entry)
		}
	}

	// Enforce max devices limit
	if len(filtered) >= MaxNotificationDevices {
		return fmt.Errorf("maximum of %d notification devices reached", MaxNotificationDevices)
	}

	filtered = append(filtered, NotificationDevice{
		DeviceID: deviceID,
		FCMToken: fcmToken,
	})

	return config.Get().SetKey("notificationDevices", filtered)
}

// Disable removes a device from push notifications
func (n *Notifications) Disable(deviceID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.removeEntry(deviceID)
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
	val, ok := config.Get().GetKey("notificationCooldownMinutes")
	if !ok {
		return 0
	}
	v, ok := val.(uint64)
	if !ok {
		return 0
	}
	return int(v)
}

// SetCooldownMinutes sets the notification cooldown in minutes (0 = off, max 30)
func (n *Notifications) SetCooldownMinutes(minutes int) error {
	if minutes < 0 || minutes > 30 {
		return fmt.Errorf("cooldown must be between 0 and 30 minutes")
	}
	return config.Get().SetKey("notificationCooldownMinutes", minutes)
}

// SendEventToAll sends an event notification to all registered devices,
// resolving each device's ProductAlias for the notification body
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
	src := n.getEntries()
	entries := make([]NotificationDevice, len(src))
	copy(entries, src)
	n.lastNotifiedAt = time.Now()
	n.mu.Unlock()

	if len(entries) == 0 {
		return
	}

	relayDomain, productID, err := n.getRelayAndProductID()
	if err != nil {
		log.Printf("Notifications: %v", err)
		return
	}

	productPrivateKey, err := config.Get().GetProductPrivateKey()
	if err != nil {
		log.Printf("Notifications: Failed to get product private key: %v", err)
		return
	}

	title := strings.ToUpper(eventType[:1]) + eventType[1:] + " detected"

	for _, entry := range entries {
		dev, ok := devices.Get().GetByID(entry.DeviceID)
		if !ok {
			log.Printf("Notifications: Skipping device %s - not found in paired devices", entry.DeviceID)
			continue
		}

		var imageURL string
		if preview != nil {
			imageURL = n.uploadEncryptedPreview(relayDomain, productPrivateKey, dev.PublicKey, preview)
		}

		go n.sendNotification(relayDomain, entry.FCMToken, title, dev.ProductAlias, productID, eventID, imageURL)
	}
}

func (n *Notifications) getRelayAndProductID() (string, string, error) {
	rd, ok := config.Get().GetKey("relayDomain")
	if !ok {
		return "", "", fmt.Errorf("relay domain not configured")
	}
	relayDomain, ok := rd.(string)
	if !ok {
		return "", "", fmt.Errorf("relay domain has invalid type")
	}

	pid, ok := config.Get().GetKey("id")
	if !ok {
		return "", "", fmt.Errorf("product ID not found in config")
	}
	productID, ok := pid.(string)
	if !ok {
		return "", "", fmt.Errorf("product ID has invalid type")
	}

	return relayDomain, productID, nil
}

func (n *Notifications) sendNotification(relayDomain, fcmToken, title, body, productID, eventID, imageURL string) {
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

	payload := map[string]any{
		"message": message,
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Notifications: Failed to marshal payload: %v", err)
		return
	}

	url := fmt.Sprintf("https://%s%s", relayDomain, sendEndpoint)
	client := &http.Client{Timeout: httpTimeout}

	resp, err := client.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		log.Printf("Notifications: Failed to send to relay: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Notifications: Relay returned status %d", resp.StatusCode)
	}
}

// uploadEncryptedPreview encrypts the preview for a specific device and uploads it to the relay
// Returns the image URL, or empty string on failure
func (n *Notifications) uploadEncryptedPreview(relayDomain string, productPrivateKey, devicePublicKey, preview []byte) string {
	session, err := rootproto.DeriveSession(productPrivateKey, devicePublicKey)
	if err != nil {
		log.Printf("Notifications: Failed to derive session: %v", err)
		return ""
	}

	encrypted, err := session.Encrypt(preview, nil)
	if err != nil {
		log.Printf("Notifications: Failed to encrypt preview: %v", err)
		return ""
	}

	uploadURL := fmt.Sprintf("https://%s%s", relayDomain, uploadPreviewEndpoint)
	client := &http.Client{Timeout: httpTimeout}

	resp, err := client.Post(uploadURL, "application/octet-stream", bytes.NewReader(encrypted))
	if err != nil {
		log.Printf("Notifications: Failed to upload preview: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Notifications: Preview upload returned status %d", resp.StatusCode)
		return ""
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Printf("Notifications: Failed to parse upload response: %v", err)
		return ""
	}

	return result.URL
}

func (n *Notifications) removeEntry(deviceID string) error {
	entries := n.getEntries()
	filtered := make([]NotificationDevice, 0, len(entries))
	for _, entry := range entries {
		if entry.DeviceID != deviceID {
			filtered = append(filtered, entry)
		}
	}
	return config.Get().SetKey("notificationDevices", filtered)
}

func (n *Notifications) getEntries() []NotificationDevice {
	val, ok := config.Get().GetKey("notificationDevices")
	if !ok {
		return []NotificationDevice{}
	}

	data, err := cbor.Marshal(val)
	if err != nil {
		log.Printf("Notifications: Failed to marshal notification devices: %v", err)
		return []NotificationDevice{}
	}
	var entries []NotificationDevice
	if err := cbor.Unmarshal(data, &entries); err != nil {
		log.Printf("Notifications: Failed to unmarshal notification devices: %v", err)
		return []NotificationDevice{}
	}
	return entries
}
