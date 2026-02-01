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
const MaxRecordDuration = 15 * time.Second // Max recording chunk duration
const LookbackDuration = 8 * time.Second   // Pre-event buffer included in recordings (must be higher than max recording duration!)

// ProductModel is the model name of the product (lowercase)
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

// Recordings
var RecordingsPath = DataDir + "/recordings"

// Event log
var EventLogPath = RecordingsPath + "/events.json"

// RAUC update configuration
const RAUCCompatible = "root-observer" // Must match system.conf and bundle manifest
