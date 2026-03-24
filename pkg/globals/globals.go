package globals

import (
	"log"
	"os"
	"strings"
	"time"
)

// FirmwareVersion is set at build time via -ldflags
var FirmwareVersion = "dev"

// HardwareModel is set at startup, e.g. "Raspberry Pi Zero 2 W Rev 1.0"
var HardwareModel string

// init runs automatically when the package is loaded
func init() {
	// Read hardware model from device tree
	data, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		log.Printf("Globals: Could not read hardware model: %v", err)
		return
	}
	HardwareModel = strings.TrimRight(string(data), "\x00")
}

// Camera
const CameraFramerate = 15      // Frames per second
const CameraGOPSize = 5         // Frames per GOP (keyframe interval)
const CameraWidth = 1920        // Horizontal pixels
const CameraHeight = 1080       // Vertical pixels
const CameraBitrate = 3_000_000 // Bits ber second

// Audio
const AudioSampleRate = 48000    // Hz
const AudioChunkSize = 48 * 1024 // Bytes per audio chunk (~500ms at 48kHz mono S16_LE)

// Streaming
const MaxConcurrentStreams = 3 // Max simultaneous stream viewers, needs to be balanced with buffers, channel sizes, relay rate limits, incoming WS rate limit etc.

// Recording
const MaxRecordDuration = 30 * time.Second // Max recording duration (higher durations take more memory)
const LookbackDuration = 8 * time.Second   // Pre-event buffer included in recordings (must be lower than the max recording duration)

// Model name lowercase
const ProductModel = "observer"

// Writable data directory
const DataDir = "/data"

// Extract embedded assets
var AssetsPath = DataDir + "/assets"

// Firmware data
var FirmwareDataDir = DataDir + "/.firmware-data"

// Config
var ConfigPath = FirmwareDataDir + "/config.json"

// Logs
var LogsPath = FirmwareDataDir + "/logs.json"

// Metrics
var MetricsPath = FirmwareDataDir + "/metrics.json"

// Recordings
var RecordingsPath = DataDir + "/recordings"

// Event log
var EventLogPath = RecordingsPath + "/events.json"

// RAUC update config
const RAUCCompatible = "root-observer" // Must match system.conf and bundle manifest
