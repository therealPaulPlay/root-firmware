package globals

// FirmwareVersion is set at build time via -ldflags
var FirmwareVersion = "dev"

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
