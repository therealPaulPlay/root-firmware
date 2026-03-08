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

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
)

const MaxNotificationDevices = 5

// NotificationDevice represents a device registered to receive push notifications
type NotificationDevice struct {
	DeviceID string `json:"deviceId"` // Must match a paired device ID
	FCMToken string `json:"fcmToken"` // Firebase Cloud Messaging token
}

type Notifications struct {
	mu sync.Mutex
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

// SendEventToAll sends an event notification to all registered devices,
// resolving each device's ProductAlias for the notification body
func (n *Notifications) SendEventToAll(eventType string, eventID string) {
	if eventType == "" {
		log.Printf("Notifications: SendEventToAll called with empty event type")
		return
	}

	n.mu.Lock()
	src := n.getEntries()
	entries := make([]NotificationDevice, len(src))
	copy(entries, src)
	n.mu.Unlock()

	if len(entries) == 0 {
		return
	}

	relayDomain, productID, err := n.getRelayAndProductID()
	if err != nil {
		log.Printf("Notifications: %v", err)
		return
	}

	title := strings.ToUpper(eventType[:1]) + eventType[1:] + " detected"

	for _, entry := range entries {
		dev, ok := devices.Get().GetByID(entry.DeviceID)
		if !ok {
			log.Printf("Notifications: Skipping device %s - not found in paired devices", entry.DeviceID)
			continue
		}
		go n.sendNotification(relayDomain, entry.FCMToken, title, dev.ProductAlias, productID, eventID)
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

func (n *Notifications) sendNotification(relayDomain, fcmToken, title, body, productID, eventID string) {
	// Build the full FCM message so the relay stays schema-agnostic
	message := map[string]any{
		"token": fcmToken,
		"notification": map[string]string{
			"title": title,
			"body":  body,
		},
		"data": map[string]string{
			"productId": productID,
			"eventId":   eventID,
		},
		"android": map[string]string{
			"priority": "high",
		},
		"apns": map[string]any{
			"headers": map[string]string{"apns-priority": "10"},
			"payload": map[string]interface{}{
				"aps": map[string]string{"sound": "default"},
			},
		},
	}

	payload := map[string]any{
		"fcmToken": fcmToken,
		"message":  message,
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Notifications: Failed to marshal payload: %v", err)
		return
	}

	url := fmt.Sprintf("https://%s/notifications/send", relayDomain)
	client := &http.Client{Timeout: 10 * time.Second}

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

	data, _ := json.Marshal(val)
	var entries []NotificationDevice
	json.Unmarshal(data, &entries)
	return entries
}
