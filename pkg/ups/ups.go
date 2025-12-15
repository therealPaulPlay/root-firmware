package ups

import (
	"fmt"
	"sync"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

const (
	ina219Addr     = 0x43
	regBusVoltage  = 0x02
	regCurrent     = 0x04
	regCalibration = 0x05
	calValue       = 26868
	currentLSB     = 0.1524
	lowPowerLevel  = 10
)

type UPS struct {
	dev i2c.Dev
}

var instance *UPS
var once sync.Once

func Init() {
	once.Do(func() {
		if _, err := host.Init(); err != nil {
			fmt.Println("Failed to initialize I2C host:", err)
			return
		}

		bus, err := i2creg.Open("")
		if err != nil {
			fmt.Println("Failed to open I2C bus:", err)
			return
		}

		dev := i2c.Dev{Bus: bus, Addr: ina219Addr}

		testRead := make([]byte, 2)
		if err := dev.Tx([]byte{regBusVoltage}, testRead); err != nil {
			fmt.Println("No UPS detected - battery monitoring disabled")
			return
		}

		instance = &UPS{
			dev: dev,
		}

		fmt.Println("UPS initialized")
	})
}

func Get() *UPS {
	return instance
}

func (u *UPS) readRegister(reg byte) int {
	write := []byte{byte(regCalibration), byte(calValue >> 8), byte(calValue & 0xFF)}
	u.dev.Write(write)

	read := make([]byte, 2)
	u.dev.Tx([]byte{reg}, read)

	value := (int(read[0]) << 8) | int(read[1])
	if value > 32767 {
		value -= 65536
	}
	return value
}

// OnACPower returns true if external power is connected
// Negative current = discharging (no external power)
// Zero or positive current = external power connected
func (u *UPS) OnACPower() bool {
	if u == nil {
		return true
	}

	value := u.readRegister(regCurrent)
	current := float64(value) * currentLSB

	return current >= 0
}

// GetBatteryPercent returns battery percentage (0-100)
func (u *UPS) GetBatteryPercent() int {
	if u == nil {
		return 100
	}

	value := u.readRegister(regBusVoltage)
	voltage := float64(value>>3) * 0.004
	percent := int((voltage - 3.0) / 1.2 * 100)

	if percent > 100 {
		return 100
	}
	if percent < 0 {
		return 0
	}
	return percent
}

// IsLowPower returns true if in low-power mode (≤10% and no external power)
// When true: Stops event detection (and automatic recordings)
func (u *UPS) IsLowPower() bool {
	if u == nil {
		return false
	}
	return u.GetBatteryPercent() <= lowPowerLevel && !u.OnACPower()
}
