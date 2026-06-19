package pairing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux"
	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
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
}

var pairingStatus operationStatus
var wifiStatus operationStatus
var relayStatus operationStatus
var wifiNetworksCache []wifi.Network
var wifiNetworksCacheMu sync.Mutex
var viewfinderChunksCache []map[string]any
var viewfinderChunksCachedAt time.Time
var viewfinderChunksCacheMu sync.Mutex
var viewfinderPreloading atomic.Bool

// writeError writes a CBOR error response to the BLE response writer
func writeError(rsp ble.ResponseWriter, message string) {
	const maxMsgLen = 70 // Leave headroom for other fields
	if len(message) > maxMsgLen {
		message = message[:maxMsgLen-3] + "..." // Truncate so that message stays below 128 bytes
	}
	data, _ := cbor.Marshal(map[string]any{"success": false, "error": message})
	rsp.Write(data)
}

// writeSuccess writes a CBOR success response with optional data fields
func writeSuccess(rsp ble.ResponseWriter, fields map[string]any) error {
	payload := map[string]any{"success": true}
	maps.Copy(payload, fields)

	data, err := cbor.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = rsp.Write(data)
	if err != nil {
		log.Printf("BLE: Write failed (%d bytes): %v", len(data), err)
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
	nameStr, ok := name.(string)
	if !ok {
		return fmt.Errorf("bluetoothName has invalid type")
	}

	// Create Linux BLE device with configured name
	d, err := linux.NewDeviceWithName(nameStr)
	if err != nil {
		return fmt.Errorf("failed to create BLE device: %w", err)
	}

	// Set as default device
	ble.SetDefaultDevice(d)

	// Create service
	svc := ble.NewService(serviceUUID)

	// Product model characteristic (read-only)
	productModelChar := svc.NewCharacteristic(productModelCharUUID)
	productModelChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if err := writeSuccess(rsp, map[string]any{"model": globals.ProductModel}); err != nil {
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
		if err := writeSuccess(rsp, map[string]any{"productId": id}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Get code characteristic (read to get pairing code)
	getCodeChar := svc.NewCharacteristic(getCodeCharUUID)
	getCodeChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		// Clear viewfinder cache when starting new pairing session
		viewfinderChunksCacheMu.Lock()
		viewfinderChunksCache = nil
		viewfinderChunksCacheMu.Unlock()

		code := GetHelper().GenerateCode()
		if err := writeSuccess(rsp, map[string]any{"code": code}); err != nil {
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
		writeSuccess(rsp, nil)
	}))

	// Viewfinder characteristic (read to get next chunk of pre-decoded preview)
	viewfinderChar := svc.NewCharacteristic(viewfinderCharUUID)
	viewfinderChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		viewfinderChunksCacheMu.Lock()
		if len(viewfinderChunksCache) == 0 || time.Since(viewfinderChunksCachedAt) > 3*time.Second {
			viewfinderChunksCache = nil
			viewfinderChunksCacheMu.Unlock()

			frameData, err := record.Get().CaptureViewfinderFrame()
			if err != nil {
				if errors.Is(err, record.ErrNoFrame) {
					writeSuccess(rsp, map[string]any{"retry": true}) // No decodable frame yet, tell client to retry
				} else {
					writeError(rsp, err.Error())
				}
				return
			}

			chunks, err := GetViewfinderChunks(frameData)
			if err != nil {
				writeError(rsp, err.Error())
				return
			}

			viewfinderChunksCacheMu.Lock()
			viewfinderChunksCache = chunks
			viewfinderChunksCachedAt = time.Now()
		}

		// Return next chunk
		chunk := viewfinderChunksCache[0]
		viewfinderChunksCache = viewfinderChunksCache[1:]
		hasMore := len(viewfinderChunksCache) > 0
		chunk["hasMore"] = hasMore
		viewfinderChunksCacheMu.Unlock()

		if err := writeSuccess(rsp, chunk); err != nil {
			writeError(rsp, err.Error())
		}

		// Pre-decode next frame in background so it's ready when client returns
		if !hasMore && viewfinderPreloading.CompareAndSwap(false, true) {
			go func() {
				defer viewfinderPreloading.Store(false)
				frameData, err := record.Get().CaptureViewfinderFrame()
				if err != nil {
					return
				}
				chunks, err := GetViewfinderChunks(frameData)
				if err != nil {
					return
				}
				viewfinderChunksCacheMu.Lock()
				if len(viewfinderChunksCache) == 0 {
					viewfinderChunksCache = chunks
					viewfinderChunksCachedAt = time.Now()
				}
				viewfinderChunksCacheMu.Unlock()
			}()
		}
	}))

	// Pair device characteristic (write to pair, read to get result)
	pairChar := svc.NewCharacteristic(pairCharUUID)
	pairChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		var pairReq struct {
			DeviceID        string `cbor:"deviceId"`
			DeviceName      string `cbor:"deviceName"`
			DevicePublicKey []byte `cbor:"devicePublicKey"`
		}

		if err := cbor.Unmarshal(req.Data(), &pairReq); err != nil {
			log.Printf("BLE: Failed to parse pair request: %v", err)
			pairingStatus = operationStatus{completed: true, success: false, error: "Failed to parse request"}
			return
		}

		if err := GetHelper().PairDevice(pairReq.DeviceID, pairReq.DeviceName, pairReq.DevicePublicKey); err != nil {
			log.Printf("BLE: Pairing failed: %v", err)
			pairingStatus = operationStatus{completed: true, success: false, error: err.Error()}
			return
		}

		pairingStatus = operationStatus{completed: true, success: true}
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
		writeSuccess(rsp, nil)
	}))

	// Get product public key characteristic (read-only)
	productPublicKeyChar := svc.NewCharacteristic(productPublicKeyCharUUID)
	productPublicKeyChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		publicKey, err := config.Get().GetProductPublicKeyP256()
		if err != nil {
			writeError(rsp, "Product public key not found")
			return
		}
		if err := writeSuccess(rsp, map[string]any{"publicKey": publicKey}); err != nil {
			log.Printf("BLE: Failed to send product public key: %v", err)
			writeError(rsp, err.Error())
		}
	}))

	// Get wifi networks characteristic - returns one network per read
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
			if len(networks) == 0 {
				writeError(rsp, "no WiFi networks found")
				return
			}
			wifiNetworksCache = networks
			log.Printf("BLE: Scanned %d WiFi networks", len(networks))
		}

		// Return next network
		network := wifiNetworksCache[0]
		wifiNetworksCache = wifiNetworksCache[1:]
		hasMore := len(wifiNetworksCache) > 0

		if err := writeSuccess(rsp, map[string]any{
			"network": network,
			"hasMore": hasMore,
		}); err != nil {
			log.Printf("BLE: Failed to send WiFi network: %v", err)
			writeError(rsp, err.Error())
		}
	}))

	// Get wifi status characteristic (read-only)
	wifiStatusChar := svc.NewCharacteristic(wifiStatusCharUUID)
	wifiStatusChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if err := writeSuccess(rsp, map[string]any{
			"connectedSSID": wifi.Get().GetCurrentNetwork(),
		}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Set wifi characteristic (write to start connection, read to poll status)
	wifiConnectChar := svc.NewCharacteristic(wifiConnectCharUUID)
	wifiConnectChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: WiFi decrypt failed: %v", err)
			wifiStatus = operationStatus{completed: true, success: false, error: "Decryption failed"}
			return
		}

		var wifiReq struct {
			SSID        string `cbor:"ssid"`
			Password    string `cbor:"password"`
			CountryCode string `cbor:"countryCode"` // Optional ISO 3166-1 alpha-2 code
		}
		if err := cbor.Unmarshal(decrypted, &wifiReq); err != nil {
			log.Printf("BLE: WiFi parse failed: %v", err)
			wifiStatus = operationStatus{completed: true, success: false, error: "Failed to parse request"}
			return
		}

		// Start connection in background, return immediately
		wifiStatus = operationStatus{completed: false}
		go func() {
			if err := wifi.Get().Connect(wifiReq.SSID, wifiReq.Password, wifiReq.CountryCode); err != nil {
				log.Printf("BLE: WiFi connection to %s failed: %v", wifiReq.SSID, err)
				wifiStatus = operationStatus{completed: true, success: false, error: err.Error()}
				return
			}
			log.Printf("BLE: WiFi configured: %s", wifiReq.SSID)
			wifiStatus = operationStatus{completed: true, success: true}
		}()
	}))
	wifiConnectChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		if !wifiStatus.completed {
			writeSuccess(rsp, map[string]any{"status": "pending"})
			return
		}
		if !wifiStatus.success {
			writeError(rsp, wifiStatus.error)
			return
		}
		writeSuccess(rsp, nil)
	}))

	// Get relay status characteristic (read-only)
	relayStatusChar := svc.NewCharacteristic(relayStatusCharUUID)
	relayStatusChar.HandleRead(ble.ReadHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		relayDomain, _ := config.Get().GetKey("relayDomain")
		if err := writeSuccess(rsp, map[string]any{
			"relayDomain": relayDomain,
		}); err != nil {
			writeError(rsp, err.Error())
		}
	}))

	// Set relay characteristic (write to configure, read to get result)
	relaySetChar := svc.NewCharacteristic(relaySetCharUUID)
	relaySetChar.HandleWrite(ble.WriteHandlerFunc(func(req ble.Request, rsp ble.ResponseWriter) {
		decrypted, err := decryptAndVerify(req.Data())
		if err != nil {
			log.Printf("BLE: Relay domain decrypt failed: %v", err)
			relayStatus = operationStatus{completed: true, success: false, error: "Decryption failed"}
			return
		}

		var relayReq struct {
			RelayDomain string `cbor:"relayDomain"`
		}
		if err := cbor.Unmarshal(decrypted, &relayReq); err != nil {
			log.Printf("BLE: Relay parse failed: %v", err)
			relayStatus = operationStatus{completed: true, success: false, error: "Failed to parse request"}
			return
		}

		// Validate relay domain (no protocol, no spaces, must have a dot)
		domain := strings.TrimRight(strings.TrimSpace(relayReq.RelayDomain), "/")
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

		// Start relay connection with new domain
		relaycomm.Get().Start()
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
		writeSuccess(rsp, nil)
	}))

	// Add service to device
	if err := ble.AddService(svc); err != nil {
		return fmt.Errorf("failed to add service: %w", err)
	}

	// Start advertising
	ctx := context.Background()
	log.Printf("BLE: Starting advertising as '%s' with service UUID %s", nameStr, serviceUUID)

	go func() {
		if err := ble.AdvertiseNameAndServices(ctx, nameStr, serviceUUID); err != nil {
			log.Printf("BLE: Advertising error: %v", err)
		} else {
			log.Printf("BLE: Advertising stopped without error")
		}
	}()

	return nil
}

func decryptAndVerify(data []byte) ([]byte, error) {
	var msg struct {
		DeviceID string `cbor:"deviceId"`
		Payload  []byte `cbor:"payload"`
	}

	if err := cbor.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("invalid payload format: %w", err)
	}

	device, ok := devices.Get().GetByID(msg.DeviceID)
	if !ok {
		return nil, fmt.Errorf("device not paired: %s", msg.DeviceID)
	}

	// Get product private key
	privKey, err := config.Get().GetProductPrivateKeyP256()
	if err != nil {
		return nil, fmt.Errorf("failed to get product private key: %w", err)
	}

	session, err := rootproto.DeriveSessionP256(privKey, device.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to derive session: %w", err)
	}

	decrypted, err := session.Decrypt(msg.Payload, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return decrypted, nil
}
