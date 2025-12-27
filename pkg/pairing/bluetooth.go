package pairing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/relaycomm"
	"root-firmware/pkg/wifi"
)

// UUIDs generated via uuidgen - these are permanent for the ROOT firmware
var (
	serviceUUID          = ble.MustParse("a07498ca-ad5b-474e-940d-16f1fbe7e8cd")
	getCodeCharUUID      = ble.MustParse("51ff12bb-3ed8-46e5-b4f9-d64e2fec021b")
	scanQRCharUUID       = ble.MustParse("2c8b0a8e-5f3d-4a9b-8e7c-1d4f6a8b9c2e")
	pairCharUUID         = ble.MustParse("4fafc201-1fb5-459e-8fcc-c5c9c331914b")
	wifiNetworksCharUUID = ble.MustParse("c2be2bc9-cee3-40ae-af50-f9959f25ee5b")
	wifiCharUUID         = ble.MustParse("beb5483e-36e1-4688-b7f5-ea07361b26a8")
	relayCharUUID        = ble.MustParse("cba1d466-344c-4be3-ab3f-189f80dd7518")
	statusCharUUID       = ble.MustParse("8d8218b6-97bc-4527-a8db-13094ac06b1d")
)

var bleDevice ble.Device
var lastPairingResult map[string]any
var lastPairingResultMu sync.Mutex
var wifiNetworksCache []wifi.Network
var wifiNetworksCacheMu sync.Mutex

// writeError writes a JSON error response to the BLE response writer
func writeError(rsp ble.ResponseWriter, message string) {
	fmt.Fprintf(rsp, `{"success":false,"error":"%s"}`, message)
}

// writeSuccess writes a JSON success response to the BLE response writer
func writeSuccess(rsp ble.ResponseWriter) {
	rsp.Write([]byte(`{"success":true}`))
}

// Init initializes the pairing system (BLE + helper)
func Init() error {
	InitHelper()
	return initBLE()
}

func initBLE() error {
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
		rsp.Write([]byte(code))
	}))

	// Scan QR characteristic (write to trigger QR scan and verification)
	scanQRChar := svc.NewCharacteristic(scanQRCharUUID)
	scanQRChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		err := GetHelper().ScanQRCode()
		if err != nil {
			log.Printf("BLE: QR scan/verification failed: %v", err)
			writeError(rsp, err.Error())
			return
		}
		log.Printf("BLE: QR code verified successfully")
		writeSuccess(rsp)
	}))

	// Pair Device characteristic (write to pair, read to get result)
	pairChar := svc.NewCharacteristic(pairCharUUID)
	pairChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		var pairReq struct {
			DeviceID        string `json:"deviceId"`
			DeviceName      string `json:"deviceName"`
			DevicePublicKey string `json:"devicePublicKey"`
		}

		if err := json.Unmarshal(req.Data(), &pairReq); err != nil {
			log.Printf("BLE: Failed to parse pair request: %v", err)
			writeError(rsp, "Invalid JSON")
			return
		}

		devicePublicKey, err := encryption.DecodePublicKey(pairReq.DevicePublicKey)
		if err != nil {
			log.Printf("BLE: Invalid public key: %v", err)
			writeError(rsp, "Invalid public key")
			return
		}

		result, err := GetHelper().PairDevice(pairReq.DeviceID, pairReq.DeviceName, devicePublicKey)
		if err != nil {
			log.Printf("BLE: Pairing failed: %v", err)
			writeError(rsp, err.Error())
			return
		}

		// Store result for subsequent read
		lastPairingResultMu.Lock()
		lastPairingResult = result
		lastPairingResultMu.Unlock()

		log.Printf("BLE: Device paired: %s (%s)", pairReq.DeviceName, pairReq.DeviceID)
		writeSuccess(rsp)
	}))
	pairChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		lastPairingResultMu.Lock()
		defer lastPairingResultMu.Unlock()

		if lastPairingResult == nil {
			rsp.Write([]byte(`{"error":"No pairing result available"}`))
			return
		}
		data, err := json.Marshal(map[string]any{"success": true, "data": lastPairingResult})
		if err != nil {
			log.Printf("BLE: Failed to marshal pairing result: %v", err)
			writeError(rsp, "Internal error")
			return
		}
		rsp.Write(data)
	}))

	// Get WiFi Networks characteristic - returns one network per read
	// Empty cache triggers scan, subsequent reads return next network
	// Reading after hasMore:false triggers new scan
	wifiNetworksChar := svc.NewCharacteristic(wifiNetworksCharUUID)
	wifiNetworksChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		wifiNetworksCacheMu.Lock()
		defer wifiNetworksCacheMu.Unlock()

		// Scan when cache is empty
		if len(wifiNetworksCache) == 0 {
			networks, err := GetHelper().ScanWiFiNetworks()
			if err != nil {
				writeError(rsp, err.Error())
				return
			}
			wifiNetworksCache = networks
			log.Printf("BLE: Scanned %d WiFi networks", len(networks))
		}

		// Return next network
		network := wifiNetworksCache[0]
		wifiNetworksCache = wifiNetworksCache[1:]
		hasMore := len(wifiNetworksCache) > 0

		data, _ := json.Marshal(map[string]any{
			"network": network,
			"hasMore": hasMore,
		})
		rsp.Write(data)
	}))

	// Set WiFi characteristic
	wifiChar := svc.NewCharacteristic(wifiCharUUID)
	wifiChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: WiFi decrypt failed: %v", err)
			writeError(rsp, err.Error())
			return
		}

		var wifiReq struct {
			SSID        string `json:"ssid"`
			Password    string `json:"password"`
			CountryCode string `json:"countryCode"` // Optional ISO 3166-1 alpha-2 code
		}
		if err := json.Unmarshal(decrypted, &wifiReq); err != nil {
			log.Printf("BLE: WiFi parse failed: %v", err)
			writeError(rsp, "Invalid payload")
			return
		}

		if err := wifi.Get().Connect(wifiReq.SSID, wifiReq.Password, wifiReq.CountryCode); err != nil {
			log.Printf("BLE: WiFi connect failed: %v", err)
			writeError(rsp, "Failed to connect to WiFi")
			return
		}

		log.Printf("BLE: WiFi configured: %s", wifiReq.SSID)
		writeSuccess(rsp)
	}))

	// Set Relay characteristic
	relayChar := svc.NewCharacteristic(relayCharUUID)
	relayChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: Relay decrypt failed: %v", err)
			writeError(rsp, err.Error())
			return
		}

		var relayReq struct {
			RelayDomain string `json:"relayDomain"`
		}
		if err := json.Unmarshal(decrypted, &relayReq); err != nil {
			log.Printf("BLE: Relay parse failed: %v", err)
			writeError(rsp, "Invalid payload")
			return
		}

		if err := config.Get().SetKey("relayDomain", relayReq.RelayDomain); err != nil {
			log.Printf("BLE: Failed to save relay config: %v", err)
			writeError(rsp, "Failed to save relay configuration")
			return
		}

		// Restart relay with new URL
		if err := relaycomm.Get().Start(); err != nil {
			log.Printf("BLE: Failed to restart relay: %v", err)
		}
		log.Printf("BLE: Relay configured: %s", relayReq.RelayDomain)
		writeSuccess(rsp)
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
		data, err := json.Marshal(status)
		if err != nil {
			log.Printf("BLE: Failed to marshal status: %v", err)
			writeError(rsp, "Internal error")
			return
		}
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

	return nil
}

func decryptAndVerify(data []byte) ([]byte, error) {
	var payload struct {
		DeviceID         string `json:"deviceId"`
		EncryptedPayload string `json:"encryptedPayload"`
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

	privKey, ok := cameraPrivateKey.([]byte)
	if !ok {
		return nil, fmt.Errorf("camera private key has invalid type")
	}

	sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive shared secret: %w", err)
	}

	session, err := encryption.FromSharedSecret(sharedSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	decrypted, err := session.Decrypt(payload.EncryptedPayload)
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
