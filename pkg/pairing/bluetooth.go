package pairing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/relaycomm"
	"root-firmware/pkg/speaker"
	"root-firmware/pkg/wifi"
)

// UUIDs generated via uuidgen - these are permanent for the ROOT firmware
var (
	serviceUUID     = ble.MustParse("a07498ca-ad5b-474e-940d-16f1fbe7e8cd")
	getCodeCharUUID = ble.MustParse("51ff12bb-3ed8-46e5-b4f9-d64e2fec021b")
	pairCharUUID    = ble.MustParse("4fafc201-1fb5-459e-8fcc-c5c9c331914b")
	wifiCharUUID    = ble.MustParse("beb5483e-36e1-4688-b7f5-ea07361b26a8")
	relayCharUUID   = ble.MustParse("cba1d466-344c-4be3-ab3f-189f80dd7518")
	statusCharUUID  = ble.MustParse("8d8218b6-97bc-4527-a8db-13094ac06b1d")
)

var bleDevice ble.Device
var lastPairingResult map[string]any

// Init initializes the pairing system (BLE + helper)
func Init() error {
	InitHelper()

	if err := InitBLE(); err != nil {
		return fmt.Errorf("failed to start BLE: %w", err)
	}

	return nil
}

func InitBLE() error {
	// Get device name for advertising
	deviceName := "ROOT-Observer"
	if ssid, ok := config.Get().GetKey("apSSID"); ok {
		deviceName = ssid.(string)
	}

	// Create Linux BLE device
	d, err := linux.NewDevice()
	if err != nil {
		return fmt.Errorf("failed to create BLE device: %w", err)
	}
	bleDevice = d

	// Set as default device
	ble.SetDefaultDevice(d)

	// Create service
	svc := ble.NewService(serviceUUID)

	// Get Code characteristic (read to get pairing code)
	getCodeChar := svc.NewCharacteristic(getCodeCharUUID)
	getCodeChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		code := GetHelper().GetCode()

		// Play sound for each digit in the code
		go func() {
			if speaker.Get() != nil {
				for _, digit := range code {
					soundFile := fmt.Sprintf("%s/sounds/numbers/%c.mp3", globals.AssetsPath, digit)
					if err := speaker.Get().PlayFile(soundFile); err != nil {
						log.Printf("BLE: Failed to play sound: %v", err)
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
		}()

		rsp.Write([]byte(code))
	}))

	// Pair Device characteristic (write to pair, read to get result)
	pairChar := svc.NewCharacteristic(pairCharUUID)
	pairChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		var pairReq struct {
			DeviceID        string `json:"deviceId"`
			DeviceName      string `json:"deviceName"`
			Code            string `json:"code"`
			DevicePublicKey string `json:"devicePublicKey"`
		}

		if err := json.Unmarshal(req.Data(), &pairReq); err != nil {
			log.Printf("BLE: Failed to parse pair request: %v", err)
			return
		}

		devicePublicKey, err := encryption.DecodePublicKey(pairReq.DevicePublicKey)
		if err != nil {
			log.Printf("BLE: Invalid public key: %v", err)
			return
		}

		result, err := GetHelper().PairDevice(pairReq.DeviceID, pairReq.DeviceName, pairReq.Code, devicePublicKey)
		if err != nil {
			log.Printf("BLE: Pairing failed: %v", err)
			return
		}

		// Store result for subsequent read
		lastPairingResult = result
		log.Printf("BLE: Device paired: %s (%s)", pairReq.DeviceName, pairReq.DeviceID)
	}))
	pairChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if lastPairingResult == nil {
			rsp.Write([]byte(`{"error":"No pairing result available"}`))
			return
		}
		data, _ := json.Marshal(map[string]any{"success": true, "data": lastPairingResult})
		rsp.Write(data)
	}))

	// Set WiFi characteristic
	wifiChar := svc.NewCharacteristic(wifiCharUUID)
	wifiChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: WiFi decrypt failed: %v", err)
			return
		}

		var wifiReq struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(decrypted, &wifiReq); err != nil {
			log.Printf("BLE: WiFi parse failed: %v", err)
			return
		}

		if err := wifi.Get().Connect(wifiReq.SSID, wifiReq.Password); err != nil {
			log.Printf("BLE: WiFi connect failed: %v", err)
			return
		}

		log.Printf("BLE: WiFi configured: %s", wifiReq.SSID)
	}))

	// Set Relay characteristic
	relayChar := svc.NewCharacteristic(relayCharUUID)
	relayChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: Relay decrypt failed: %v", err)
			return
		}

		var relayReq struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(decrypted, &relayReq); err != nil {
			log.Printf("BLE: Relay parse failed: %v", err)
			return
		}

		if err := config.Get().SetKey("relayDomain", relayReq.URL); err != nil {
			log.Printf("BLE: Failed to save relay config: %v", err)
			return
		}

		// Restart relay with new URL
		if err := relaycomm.Get().Start(); err != nil {
			log.Printf("BLE: Failed to restart relay: %v", err)
		}
		log.Printf("BLE: Relay configured: %s", relayReq.URL)
	}))

	// Get Status characteristic (read to get device status)
	statusChar := svc.NewCharacteristic(statusCharUUID)
	statusChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		relayDomain, _ := config.Get().GetKey("relayDomain")
		status := map[string]any{
			"version":       globals.FirmwareVersion,
			"wifiConnected": wifi.Get().IsConnected(),
			"relayDomain":   relayDomain,
		}
		data, _ := json.Marshal(status)
		rsp.Write(data)
	}))

	// Add service to device
	if err := ble.AddService(svc); err != nil {
		return fmt.Errorf("failed to add service: %w", err)
	}

	// Start advertising
	ctx := context.Background()
	log.Printf("BLE: Starting advertising as '%s' with service UUID %s", deviceName, serviceUUID)

	go func() {
		log.Printf("BLE: Advertising goroutine started")
		if err := ble.AdvertiseNameAndServices(ctx, deviceName, serviceUUID); err != nil {
			log.Printf("BLE: Advertising error: %v", err)
		} else {
			log.Printf("BLE: Advertising stopped normally")
		}
	}()

	// Give advertising a moment to start
	time.Sleep(100 * time.Millisecond)
	log.Printf("BLE: Initialization complete")
	return nil
}

func decryptAndVerify(data []byte) ([]byte, error) {
	var payload struct {
		DeviceID string `json:"deviceId"`
		Data     string `json:"data"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("invalid payload format: %w", err)
	}

	device, ok := devices.Get().GetByID(payload.DeviceID)
	if !ok {
		return nil, fmt.Errorf("device not paired: %s", payload.DeviceID)
	}

	// Get camera private key
	cameraPrivateKey, ok := config.Get().GetKey("cameraPrivateKey")
	if !ok {
		return nil, fmt.Errorf("camera private key not found")
	}

	sharedSecret, err := encryption.DeriveSharedSecret(cameraPrivateKey.([]byte), device.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive shared secret: %w", err)
	}

	session, err := encryption.FromSharedSecret(sharedSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	decrypted, err := session.Decrypt(payload.Data)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return decrypted, nil
}

func GetBLE() ble.Device {
	return bleDevice
}

func StopBLE() {
	if bleDevice != nil {
		bleDevice.Stop()
	}
}
