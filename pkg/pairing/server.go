package pairing

import (
	"encoding/json"
	"net/http"
	"sync"

	"root-firmware/pkg/config"
	"root-firmware/pkg/wifi"
)

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

	// Pairing endpoint
	mux.HandleFunc("/pair", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			DeviceID        string `json:"deviceId"`
			DeviceName      string `json:"deviceName"`
			Code            string `json:"code"`
			DevicePublicKey string `json:"devicePublicKey"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		result, err := GetHelper().PairDevice(
			req.DeviceID,
			req.DeviceName,
			req.Code,
			[]byte(req.DevicePublicKey),
		)

		if err != nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"data":    result,
		})
	})

	// WiFi connection endpoint
	mux.HandleFunc("/set-wifi", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			SSID     string `json:"ssid"`
			Password string `json:"password"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		err := wifi.Get().Connect(req.SSID, req.Password)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": err == nil,
			"error":   err,
		})
	})

	// Relay server setup endpoint
	mux.HandleFunc("/set-relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			RelayURL string `json:"relayUrl"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		err := config.Get().SetKey("relayUrl", req.RelayURL)

		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": err == nil,
			"error":   err,
		})
	})

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
