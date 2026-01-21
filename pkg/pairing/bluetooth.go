package pairing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/globals"
	"root-firmware/pkg/record"
	"root-firmware/pkg/relaycomm"
	"root-firmware/pkg/wifi"
)

// UUIDs generated via uuidgen - these are permanent for the ROOT firmware
var (
	serviceUUID              = ble.MustParse("a07498ca-ad5b-474e-940d-16f1fbe7e8cd")
	productModelCharUUID     = ble.MustParse("fa3fd066-de2d-4a15-8e0c-4a8d45a847a5")
	productIdCharUUID        = ble.MustParse("8f3c4d5e-9a2b-4f1e-8d6c-7e5f4a3b2c1d")
	getCodeCharUUID          = ble.MustParse("51ff12bb-3ed8-46e5-b4f9-d64e2fec021b")
	scanQRCharUUID           = ble.MustParse("2c8b0a8e-5f3d-4a9b-8e7c-1d4f6a8b9c2e")
	viewfinderCharUUID       = ble.MustParse("3d9e1f7a-4b6c-5e8d-9f0a-1b2c3d4e5f6a")
	pairCharUUID             = ble.MustParse("4fafc201-1fb5-459e-8fcc-c5c9c331914b")
	productPublicKeyCharUUID = ble.MustParse("2d7c0e8f-5a3b-4c1d-8e6a-0f4b9d2c7e1a")
	wifiNetworksCharUUID     = ble.MustParse("c2be2bc9-cee3-40ae-af50-f9959f25ee5b")
	wifiStatusCharUUID       = ble.MustParse("d96453d5-1f49-47d6-8cbd-ac5547fc51a9")
	wifiConnectCharUUID      = ble.MustParse("beb5483e-36e1-4688-b7f5-ea07361b26a8")
	relayStatusCharUUID      = ble.MustParse("a9988b7b-e4ea-49b1-b9d1-548aeb0ec5ab")
	relaySetCharUUID         = ble.MustParse("cba1d466-344c-4be3-ab3f-189f80dd7518")
)

type operationStatus struct {
	completed bool
	success   bool
	error     string
	data      map[string]any
}

var bleDevice ble.Device

var pairingStatus operationStatus
var wifiStatus operationStatus
var relayStatus operationStatus
var wifiNetworksCache []wifi.Network
var wifiNetworksCacheMu sync.Mutex
var viewfinderChunksCache []map[string]any
var viewfinderChunksCacheMu sync.Mutex

// writeError writes a JSON error response to the BLE response writer
func writeError(rsp ble.ResponseWriter, message string) {
	fmt.Fprintf(rsp, `{"success":false,"error":"%s"}`, message)
}

// writeSuccess writes a JSON success response to the BLE response writer
func writeSuccess(rsp ble.ResponseWriter) {
	rsp.Write([]byte(`{"success":true}`))
}

// writeJSON marshals and writes JSON to BLE
func writeJSON(rsp ble.ResponseWriter, data map[string]any) error {
	response := map[string]any{"success": true}
	maps.Copy(response, data)

	jsonData, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = rsp.Write(jsonData)
	if err != nil {
		log.Printf("BLE: Write failed (%d bytes): %v", len(jsonData), err)
	}
	return err
}

// Init initializes the pairing system (BLE + helper)
func Init() error {
	InitHelper()
	return initBLE()
}

func initBLE() error {
	// Get bluetooth name from config
	name, ok := config.Get().GetKey("bluetoothName")
	if !ok {
		return fmt.Errorf("bluetoothName not set in config")
	}

	// Create Linux BLE device with configured name
	d, err := linux.NewDeviceWithName(name.(string))
	if err != nil {
		return fmt.Errorf("failed to create BLE device: %w", err)
	}
	bleDevice = d

	// Set as default device
	ble.SetDefaultDevice(d)

	// Create service
	svc := ble.NewService(serviceUUID)

	// Product model characteristic (read-only)
	productModelChar := svc.NewCharacteristic(productModelCharUUID)
	productModelChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if err := writeJSON(rsp, map[string]any{"model": globals.ProductModel}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Product ID characteristic (read-only)
	productIdChar := svc.NewCharacteristic(productIdCharUUID)
	productIdChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		id, ok := config.Get().GetKey("id")
		if !ok {
			writeError(rsp, "Config not initialized - ID not set")
			return
		}
		if err := writeJSON(rsp, map[string]any{"productId": id}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Get Code characteristic (read to get pairing code)
	getCodeChar := svc.NewCharacteristic(getCodeCharUUID)
	getCodeChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		// Clear viewfinder cache when starting new pairing session
		viewfinderChunksCacheMu.Lock()
		viewfinderChunksCache = nil
		viewfinderChunksCacheMu.Unlock()

		code := GetHelper().GenerateCode()
		if err := writeJSON(rsp, map[string]any{"code": code}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Scan QR characteristic (read to trigger QR scan and verification)
	scanQRChar := svc.NewCharacteristic(scanQRCharUUID)
	scanQRChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		err := GetHelper().ScanQRCode()
		if err != nil {
			log.Printf("BLE: QR scan/verification failed: %v", err)
			writeError(rsp, err.Error())
			return
		}
		log.Printf("BLE: QR code verified successfully")
		writeSuccess(rsp)
	}))

	// Viewfinder characteristic (read to get next chunk of 48x48 2-bit preview)
	viewfinderChar := svc.NewCharacteristic(viewfinderCharUUID)
	viewfinderChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		viewfinderChunksCacheMu.Lock()
		if len(viewfinderChunksCache) == 0 {
			viewfinderChunksCacheMu.Unlock()

			frameData, err := record.Get().CaptureViewfinderFrame(viewfinderWidth, viewfinderHeight)
			if err != nil {
				writeError(rsp, err.Error())
				return
			}

			chunks, err := GetViewfinderChunks(frameData)
			if err != nil {
				writeError(rsp, err.Error())
				return
			}

			viewfinderChunksCacheMu.Lock()
			viewfinderChunksCache = chunks
		}

		// Return next chunk
		chunk := viewfinderChunksCache[0]
		viewfinderChunksCache = viewfinderChunksCache[1:]
		chunk["hasMore"] = len(viewfinderChunksCache) > 0
		viewfinderChunksCacheMu.Unlock()

		if err := writeJSON(rsp, chunk); err != nil {
			writeError(rsp, err.Error())
		}
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
			pairingStatus = operationStatus{completed: true, success: false, error: "Failed to parse request"}
			return
		}

		devicePublicKey, err := encryption.DecodeKey(pairReq.DevicePublicKey)
		if err != nil {
			log.Printf("BLE: Invalid public key: %v", err)
			pairingStatus = operationStatus{completed: true, success: false, error: "Invalid public key"}
			return
		}

		result, err := GetHelper().PairDevice(pairReq.DeviceID, pairReq.DeviceName, devicePublicKey)
		if err != nil {
			log.Printf("BLE: Pairing failed: %v", err)
			pairingStatus = operationStatus{completed: true, success: false, error: err.Error()}
			return
		}

		pairingStatus = operationStatus{completed: true, success: true, data: result}
		log.Printf("BLE: Device paired: %s (%s)", pairReq.DeviceName, pairReq.DeviceID)
	}))
	pairChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if !pairingStatus.completed {
			writeError(rsp, "No pairing result available")
			return
		}
		if !pairingStatus.success {
			writeError(rsp, pairingStatus.error)
			return
		}
		log.Printf("BLE: Sending pairing result: %+v", pairingStatus.data)
		// Return only productId (camera public key is in separate characteristic)
		if err := writeJSON(rsp, map[string]any{"productId": pairingStatus.data["productId"]}); err != nil {
			log.Printf("BLE: Failed to send pairing result: %v", err)
			writeError(rsp, err.Error())
		}
	}))

	// Get Camera Public Key characteristic (read after pairing)
	cameraPublicKeyChar := svc.NewCharacteristic(productPublicKeyCharUUID)
	cameraPublicKeyChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if !pairingStatus.completed {
			writeError(rsp, "No pairing result available")
			return
		}
		if !pairingStatus.success {
			writeError(rsp, pairingStatus.error)
			return
		}
		if err := writeJSON(rsp, map[string]any{"publicKey": pairingStatus.data["publicKey"]}); err != nil {
			log.Printf("BLE: Failed to send camera public key: %v", err)
			writeError(rsp, err.Error())
		}
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

		if err := writeJSON(rsp, map[string]any{
			"network": network,
			"hasMore": hasMore,
		}); err != nil {
			log.Printf("BLE: Failed to send WiFi network: %v", err)
			writeError(rsp, err.Error())
		}
	}))

	// Get WiFi Status characteristic (read-only)
	wifiStatusChar := svc.NewCharacteristic(wifiStatusCharUUID)
	wifiStatusChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if err := writeJSON(rsp, map[string]any{
			"connected": wifi.Get().IsConnected(),
			"ssid":      wifi.Get().GetCurrentNetwork(),
		}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Set WiFi characteristic (write to configure, read to get result)
	wifiConnectChar := svc.NewCharacteristic(wifiConnectCharUUID)
	wifiConnectChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: WiFi decrypt failed: %v", err)
			wifiStatus = operationStatus{completed: true, success: false, error: "Decryption failed"}
			return
		}

		var wifiReq struct {
			SSID        string `json:"ssid"`
			Password    string `json:"password"`
			CountryCode string `json:"countryCode"` // Optional ISO 3166-1 alpha-2 code
		}
		if err := json.Unmarshal(decrypted, &wifiReq); err != nil {
			log.Printf("BLE: WiFi parse failed: %v", err)
			wifiStatus = operationStatus{completed: true, success: false, error: "Failed to parse request"}
			return
		}

		if err := wifi.Get().Connect(wifiReq.SSID, wifiReq.Password, wifiReq.CountryCode); err != nil {
			log.Printf("BLE: WiFi connection to %s failed: %v", wifiReq.SSID, err)
			wifiStatus = operationStatus{completed: true, success: false, error: err.Error()}
			return
		}

		log.Printf("BLE: WiFi configured: %s", wifiReq.SSID)
		wifiStatus = operationStatus{completed: true, success: true}
	}))
	wifiConnectChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if !wifiStatus.completed {
			writeError(rsp, "No WiFi configuration result available")
			return
		}
		if !wifiStatus.success {
			writeError(rsp, wifiStatus.error)
			return
		}
		writeSuccess(rsp)
	}))

	// Get Relay status characteristic (read-only)
	relayStatusChar := svc.NewCharacteristic(relayStatusCharUUID)
	relayStatusChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		relayDomain, _ := config.Get().GetKey("relayDomain")
		if err := writeJSON(rsp, map[string]any{
			"relayDomain": relayDomain,
		}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Set Relay characteristic (write to configure, read to get result)
	relaySetChar := svc.NewCharacteristic(relaySetCharUUID)
	relaySetChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: Relay domain decrypt failed: %v", err)
			relayStatus = operationStatus{completed: true, success: false, error: "Decryption failed"}
			return
		}

		var relayReq struct {
			RelayDomain string `json:"relayDomain"`
		}
		if err := json.Unmarshal(decrypted, &relayReq); err != nil {
			log.Printf("BLE: Relay parse failed: %v", err)
			relayStatus = operationStatus{completed: true, success: false, error: "Failed to parse request"}
			return
		}

		// Validate relay domain (no protocol, no spaces, must have a dot)
		domain := strings.TrimSpace(relayReq.RelayDomain)
		if domain == "" || strings.Contains(domain, "://") || strings.Contains(domain, " ") || !strings.Contains(domain, ".") {
			log.Printf("BLE: Invalid relay domain: %s", domain)
			relayStatus = operationStatus{completed: true, success: false, error: "Invalid relay domain"}
			return
		}

		if err := config.Get().SetKey("relayDomain", domain); err != nil {
			log.Printf("BLE: Failed to save relay config: %v", err)
			relayStatus = operationStatus{completed: true, success: false, error: err.Error()}
			return
		}

		// Restart relay with new URL
		if err := relaycomm.Get().Start(); err != nil {
			log.Printf("BLE: Failed to restart relay: %v", err)
		}
		log.Printf("BLE: Relay configured: %s", relayReq.RelayDomain)
		relayStatus = operationStatus{completed: true, success: true}
	}))
	relaySetChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if !relayStatus.completed {
			writeError(rsp, "No relay configuration result available")
			return
		}
		if !relayStatus.success {
			writeError(rsp, relayStatus.error)
			return
		}
		writeSuccess(rsp)
	}))

	// Add service to device
	if err := ble.AddService(svc); err != nil {
		return fmt.Errorf("failed to add service: %w", err)
	}

	// Start advertising
	ctx := context.Background()
	log.Printf("BLE: Starting advertising as '%s' with service UUID %s", name.(string), serviceUUID)

	go func() {
		if err := ble.AdvertiseNameAndServices(ctx, name.(string), serviceUUID); err != nil {
			log.Printf("BLE: Advertising error: %v", err)
		} else {
			log.Printf("BLE: Advertising stopped without error")
		}
	}()

	return nil
}

func decryptAndVerify(data []byte) ([]byte, error) {
	var msg struct {
		DeviceID string `json:"deviceId"`
		Payload  string `json:"payload"`
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("invalid payload format: %w", err)
	}

	device, ok := devices.Get().GetByID(msg.DeviceID)
	if !ok {
		return nil, fmt.Errorf("device not paired: %s", msg.DeviceID)
	}

	// Get camera private key (stored as base64 string)
	cameraPrivateKeyEncoded, ok := config.Get().GetKey("cameraPrivateKey")
	if !ok {
		return nil, fmt.Errorf("camera private key not found")
	}

	privKeyStr, ok := cameraPrivateKeyEncoded.(string)
	if !ok {
		return nil, fmt.Errorf("camera private key has invalid type")
	}

	// Decode from base64
	privKey, err := encryption.DecodeKey(privKeyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode camera private key: %w", err)
	}

	sharedSecret, err := encryption.DeriveSharedSecret(privKey, device.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive shared secret: %w", err)
	}

	session, err := encryption.FromSharedSecret(sharedSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	decrypted, err := session.Decrypt(msg.Payload)
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
