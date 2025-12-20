package globals

// FirmwareVersion is set at build time via -ldflags
var FirmwareVersion = "dev"

// Writable data directory
var DataDir = "/data"

// Extract embedded assets
var AssetsPath = DataDir + "/assets"

// Firmware data
var FirmwareDataDir = DataDir + "/.firmware-data"

// Config
var ConfigPath = FirmwareDataDir + "/config.json"

// Logs
var LogsPath = FirmwareDataDir + "/logs.json"

// WpaSupplicantPath for WiFi credentials
var WpaSupplicantPath = "/etc/wpa_supplicant/wpa_supplicant.conf"

// Recordings
var RecordingsPath = DataDir + "/recordings"

// Event log
var EventLogPath = RecordingsPath + "/events.json"

// Firmware update paths
var UpdateImagePath = "/tmp/firmware-update.img"      // Temporary download, cleaned on reboot
var BootCmdlinePath = "/boot/firmware/cmdline.txt"    // Boot partition configuration
var BootCountPath = "/boot/firmware/bootcount.txt"    // Boot attempt counter for rollback

// Partition configuration for A/B updates
var PartitionA = "/dev/mmcblk0p2"
var PartitionB = "/dev/mmcblk0p3"

// Boot counter configuration
const MaxBootAttempts = 3 // Number of boot attempts before rollback
