package relaycomm

import (
	"os"
	"sync"

	rootproto "github.com/therealPaulPlay/root-e2ee-protocol/go-server"

	"root-firmware/pkg/fsutil"
	"root-firmware/pkg/globals"
)

// persistentReplayStore returns a ReplayStore backed by the firmware replay state file
// Append uses O_APPEND + fsync so accepted messages are durable before the next request
// Save uses atomic write so the snapshot fully replaces prior state
func persistentReplayStore() rootproto.ReplayStore {
	var mu sync.Mutex
	path := globals.ReplayStatePath

	ensureDir := func() error {
		return os.MkdirAll(globals.FirmwareDataDir, 0755)
	}

	return rootproto.ReplayStore{
		Load: func() ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			if err := ensureDir(); err != nil {
				return nil, err
			}
			bytes, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return nil, nil
			}
			return bytes, err
		},
		Append: func(entry []byte) error {
			mu.Lock()
			defer mu.Unlock()
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := f.Write(entry); err != nil {
				return err
			}
			return f.Sync()
		},
		Save: func(snapshot []byte) error {
			mu.Lock()
			defer mu.Unlock()
			return fsutil.AtomicWrite(path, snapshot, 0600)
		},
	}
}
