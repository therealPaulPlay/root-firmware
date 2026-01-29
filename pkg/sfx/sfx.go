package sfx

import (
	"log"
	"sync"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

type SFX struct {
	pin gpio.PinIO
	mu  sync.Mutex
}

var instance *SFX
var once sync.Once

// Init initializes the SFX system
func Init() error {
	var initErr error
	once.Do(func() {
		// Initialize periph host
		if _, err := host.Init(); err != nil {
			initErr = err
			return
		}

		// Get GPIO pin (using GPIO18 / Pin 12 by default)
		pin := gpioreg.ByName("GPIO18")
		if pin == nil {
			log.Println("SFX: GPIO18 not found - SFX disabled")
			instance = &SFX{pin: nil}
			return
		}

		instance = &SFX{pin: pin}
	})
	return initErr
}

// Get returns the SFX instance
func Get() *SFX {
	if instance == nil {
		panic("sfx not initialized - call Init() first")
	}
	return instance
}

// playTone generates a square wave at the specified frequency for the given duration.
// Uses busy-wait for precise timing instead of time.Sleep which has too much jitter.
func (s *SFX) playTone(frequency int, duration time.Duration) {
	if s.pin == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	halfPeriod := time.Second / time.Duration(frequency) / 2
	end := time.Now().Add(duration)

	for time.Now().Before(end) {
		s.pin.Out(gpio.High)
		spinWait(halfPeriod)
		s.pin.Out(gpio.Low)
		spinWait(halfPeriod)
	}
}

func spinWait(d time.Duration) {
	target := time.Now().Add(d)
	for time.Now().Before(target) {
	}
}

// PlayStartup plays a startup sound (two ascending tones)
func (s *SFX) PlayStartup() {
	if s.pin == nil {
		return
	}

	go func() {
		s.playTone(300, 100*time.Millisecond)
		time.Sleep(10 * time.Millisecond)
		s.playTone(400, 100*time.Millisecond)
	}()
}

// PlayRecording plays a sound to indicate that recording is starting
func (s *SFX) PlayRecording() {
	if s.pin == nil {
		return
	}

	go func() {
		s.playTone(500, 60*time.Millisecond)
		time.Sleep(40 * time.Millisecond)
		s.playTone(500, 60*time.Millisecond)
	}()
}

// PlayPairingSuccess plays a sound to indicate successful device pairing
func (s *SFX) PlayPairingSuccess() {
	if s.pin == nil {
		return
	}

	go func() {
		s.playTone(600, 50*time.Millisecond)
	}()
}
