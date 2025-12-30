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

// Firmware update paths
const UpdateImagePath = "/tmp/firmware-update.img"   // Temporary download, cleaned on reboot
const BootCmdlinePath = "/boot/firmware/cmdline.txt" // Boot partition configuration
const BootCountPath = "/boot/firmware/bootcount.txt" // Boot attempt counter for rollback

// Boot counter configuration
const MaxBootAttempts = 3 // Number of boot attempts before rollback
