package pairing

import (
	"encoding/json"
	"net/http"
	"sync"

	"root-firmware/pkg/config"
	"root-firmware/pkg/devices"
	"root-firmware/pkg/encryption"
	"root-firmware/pkg/wifi"
)

// EncryptedRequest wraps encrypted payloads from devices for HTTP endpoints
type EncryptedRequest struct {
	DeviceID         string `json:"deviceId"`
	EncryptedPayload string `json:"encryptedPayload"` // Base64 encrypted JSON
}

// withEncryption is middleware for HTTP endpoints that require device authentication and E2E encryption
func withEncryption(handler func(w http.ResponseWriter, decrypted []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Method not allowed"})
			return
		}

		// Parse encrypted request
		var req EncryptedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid request"})
			return
		}

		// Verify device is paired
		device, ok := devices.Get().GetByID(req.DeviceID)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Device not paired"})
			return
		}

		// Get camera's private key (single key for all devices)
		cameraPrivateKey, ok := config.Get().GetKey("cameraPrivateKey")
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Camera not initialized"})
			return
		}

		// Derive shared secret using camera's private key and device's public key
		sharedSecret, err := encryption.DeriveSharedSecret(cameraPrivateKey.([]byte), device.PublicKey)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Failed to derive key"})
			return
		}

		// Create session for decryption
		session, err := encryption.FromSharedSecret(sharedSecret)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Failed to create session"})
			return
		}

		// Decrypt payload
		decrypted, err := session.Decrypt(req.EncryptedPayload)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Failed to decrypt payload"})
			return
		}

		// Call actual handler with decrypted payload
		handler(w, decrypted)
	}
}

type Server struct {
	server *http.Server
}

var serverInstance *Server
var serverOnce sync.Once

// InitServer starts a simple HTTP server for pairing
// The camera broadcasts its own WiFi access point, and the phone connects via HTTP
func InitServer(port string) error {
	var err error
	serverOnce.Do(func() {
		serverInstance = &Server{}
		err = serverInstance.start(port)
	})
	return err
}

func GetServer() *Server {
	if serverInstance == nil {
		panic("server not initialized - call InitServer() first")
	}
	return serverInstance
}

func (s *Server) start(port string) error {
	mux := http.NewServeMux()

	// Get pairing code endpoint
	mux.HandleFunc("/get-code", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		code := GetHelper().GetCode()
		json.NewEncoder(w).Encode(map[string]interface{}{"code": code})
	})

	// Pairing endpoint
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Method not allowed"})
			return
		}

		var req struct {
			DeviceID        string `json:"deviceId"`
			DeviceName      string `json:"deviceName"`
			Code            string `json:"code"`
			DevicePublicKey string `json:"devicePublicKey"` // Base64 encoded
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		// Decode base64 public key
		devicePublicKey, err := encryption.DecodePublicKey(req.DevicePublicKey)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid public key format"})
			return
		}

		result, err := GetHelper().PairDevice(req.DeviceID, req.DeviceName, req.Code, devicePublicKey)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "data": result})
	})

	// WiFi connection endpoint (requires paired device with encrypted payload)
	mux.HandleFunc("/set-wifi", withEncryption(func(w http.ResponseWriter, decrypted []byte) {
		var wifiReq struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		}
		if err := json.Unmarshal(decrypted, &wifiReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid payload"})
			return
		}

		if err := wifi.Get().Connect(wifiReq.SSID, wifiReq.Password); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))

	// Relay server setup endpoint (requires paired device with encrypted payload)
	mux.HandleFunc("/set-relay", withEncryption(func(w http.ResponseWriter, decrypted []byte) {
		var relayReq struct {
			RelayURL string `json:"relayUrl"`
		}
		if err := json.Unmarshal(decrypted, &relayReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": "Invalid payload"})
			return
		}

		if err := config.Get().SetKey("relayUrl", relayReq.RelayURL); err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
	}))

	s.server = &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	go s.server.ListenAndServe()
	return nil
}

func (s *Server) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}
