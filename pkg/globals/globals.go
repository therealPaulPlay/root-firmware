package globals

// DataDir is the root writable data directory
var DataDir = "/data"

// AssetsPath is the directory where embedded assets are extracted
var AssetsPath = DataDir + "/assets"

// FirmwareDataDir is the writable directory for persistent firmware data
var FirmwareDataDir = DataDir + "/.firmware-data"

// ConfigPath is the path to the firmware configuration file
var ConfigPath = FirmwareDataDir + "/config.json"

// WpaSupplicantPath is the system path to WiFi credentials file (must be writable)
var WpaSupplicantPath = "/etc/wpa_supplicant/wpa_supplicant.conf"

// RecordingsPath is the directory where video recordings are stored
var RecordingsPath = DataDir + "/recordings"

// EventLogPath is the path to the event log JSON file
var EventLogPath = RecordingsPath + "/events.json"
