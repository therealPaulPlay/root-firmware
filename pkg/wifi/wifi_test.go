package wifi

import (
	"sync"
	"testing"
)

func resetSingletons() {
	instance = nil
	once = sync.Once{}
}

func TestParseNetworks(t *testing.T) {
	w := &WiFi{}

	// Real nmcli output format
	output := `DIRECT-7F-Printer:74:WPA2:2437 MHz
FastWiFi-5G:73:WPA1 WPA2:2462 MHz
SlowWiFi:39:WPA1 WPA2:2417 MHz
OpenCafe:45::2412 MHz
--:50:WPA2:2437 MHz
:40::2437 MHz`

	networks := w.parseNetworks(output)

	if len(networks) != 4 {
		t.Fatalf("parseNetworks() = %d networks, want 4", len(networks))
	}

	// Sorted by signal (highest first): 74, 73, 45, 39
	if networks[0].SSID != "DIRECT-7F-Printer" || networks[0].Signal != 74 || !networks[0].Secured {
		t.Errorf("networks[0] = %+v", networks[0])
	}
	// OpenCafe is open (empty security field)
	if networks[2].SSID != "OpenCafe" || networks[2].Signal != 45 || networks[2].Secured {
		t.Errorf("networks[2] = %+v, want OpenCafe/45/open", networks[2])
	}
}

func TestParseNetworks_DeduplicatesBySignal(t *testing.T) {
	w := &WiFi{}
	output := `HomeNetwork:60:WPA2:2437 MHz
HomeNetwork:85:WPA2:5180 MHz
HomeNetwork:72:WPA2:2462 MHz`

	networks := w.parseNetworks(output)

	if len(networks) != 1 || networks[0].Signal != 85 {
		t.Errorf("got %+v, want single network with signal 85", networks)
	}
}

func TestParseNetworks_Empty(t *testing.T) {
	w := &WiFi{}
	if networks := w.parseNetworks(""); len(networks) != 0 {
		t.Errorf("parseNetworks(\"\") = %d networks, want 0", len(networks))
	}
}

func TestGet_PanicsWithoutInit(t *testing.T) {
	resetSingletons()
	defer resetSingletons()

	defer func() {
		if r := recover(); r == nil {
			t.Error("Get() should panic when not initialized")
		}
	}()

	Get()
}
