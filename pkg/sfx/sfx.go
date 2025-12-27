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
			log.Println("Warning: GPIO18 not found - SFX disabled")
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

// playTone generates a square wave at the specified frequency for the given duration
func (s *SFX) playTone(frequency int, duration time.Duration) {
	if s.pin == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	period := time.Second / time.Duration(frequency)
	halfPeriod := period / 2
	cycles := int(duration / period)

	for range cycles {
		s.pin.Out(gpio.High)
		time.Sleep(halfPeriod)
		s.pin.Out(gpio.Low)
		time.Sleep(halfPeriod)
	}

	s.pin.Out(gpio.Low)
}

// PlayStartup plays a startup sound (three ascending tones like a boot-up sequence)
func (s *SFX) PlayStartup() {
	if s.pin == nil {
		return
	}

	go func() {
		s.playTone(400, 120*time.Millisecond)
		time.Sleep(40 * time.Millisecond)
		s.playTone(500, 120*time.Millisecond)
		time.Sleep(40 * time.Millisecond)
		s.playTone(600, 120*time.Millisecond)
	}()
}

// PlayRecording plays a sound to indicate that recording is starting
func (s *SFX) PlayRecording() {
	if s.pin == nil {
		return
	}

	go func() {
		s.playTone(600, 100*time.Millisecond)
		time.Sleep(30 * time.Millisecond)
		s.playTone(800, 100*time.Millisecond)
	}()
}
