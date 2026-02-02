package globals

import "time"

// FirmwareVersion is set at build time via -ldflags
var FirmwareVersion = "dev"

// Camera
const CameraFramerate = 15 // Frames per second
const CameraGOPSize = 5    // Frames per GOP (keyframe interval)

// Audio
const AudioSampleRate = 48000    // Hz
const AudioChunkSize = 48 * 1024 // Bytes per audio chunk (~500ms at 48kHz mono S16_LE)

// Recording
const MaxRecordDuration = 20 * time.Second // Max recording duration
const LookbackDuration = 8 * time.Second   // Pre-event buffer included in recordings (must be higher than max rec. duration)

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
