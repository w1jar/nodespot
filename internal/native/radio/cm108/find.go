package cm108

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/w1jar/nodespot/internal/native/radio/usbsys"
)

// chip is one row of the table of sound chips that work as a radio
// interface: the same C-Media family the AllStarLink simpleusb channel
// driver accepts. Mask selects which product id bits are compared; 0 means
// all of them.
type chip struct {
	ID   usbsys.ID
	Mask uint16
	Name string
}

const cmedia = 0x0d8c

var chips = []chip{
	{usbsys.ID{Vendor: cmedia, Product: 0x000c}, 0, "CM108"},
	{usbsys.ID{Vendor: cmedia, Product: 0x000d}, 0, "CM108"},
	{usbsys.ID{Vendor: cmedia, Product: 0x000e}, 0, "CM109"},
	{usbsys.ID{Vendor: cmedia, Product: 0x000f}, 0, "CM109"},
	{usbsys.ID{Vendor: cmedia, Product: 0x0008}, 0, "CM119"},
	{usbsys.ID{Vendor: cmedia, Product: 0x0012}, 0, "CM108B"},
	{usbsys.ID{Vendor: cmedia, Product: 0x0013}, 0, "CM119B"},
	{usbsys.ID{Vendor: cmedia, Product: 0x013a}, 0, "CM119A"},
	{usbsys.ID{Vendor: cmedia, Product: 0x013c}, 0, "CM108AH"},
	// Boards from small makers that reuse the C-Media vendor id and put
	// their own product id in the 0x6a00 to 0x6aff range.
	{usbsys.ID{Vendor: cmedia, Product: 0x6a00}, 0xff00, "CM108-compatible"},
}

// SupportedIDs lists one USB id for each row of the chip table; the last row
// stands for the whole 0x6a00 to 0x6aff range. Use [Identify] to test an id.
var SupportedIDs = func() []usbsys.ID {
	out := make([]usbsys.ID, len(chips))
	for i, c := range chips {
		out[i] = c.ID
	}
	return out
}()

// Identify returns the chip name for a USB id and whether it is a supported
// interface chip.
func Identify(id usbsys.ID) (string, bool) {
	for _, c := range chips {
		mask := c.Mask
		if mask == 0 {
			mask = 0xffff
		}
		if id.Vendor == c.ID.Vendor && id.Product&mask == c.ID.Product&mask {
			return c.Name, true
		}
	}
	return "", false
}

// Supported reports whether id is one of the supported chips.
func Supported(id usbsys.ID) bool { _, ok := Identify(id); return ok }

// ErrNotFound is returned when no supported sound chip is present.
var ErrNotFound = errors.New("cm108: no supported USB sound interface found")

// Info describes a detected interface.
type Info struct {
	// BusPath is the USB bus path, such as "1-1.2".
	BusPath string
	ID      usbsys.ID
	// Chip is the chip family ("CM108B", "CM119A") and Product the USB
	// product string, empty if the device has none.
	Chip    string
	Product string
	// Hidraw is the device node, such as /dev/hidraw0.
	Hidraw string
	// Card is the ALSA card number; capture and playback are device 0.
	Card int
}

// Discover finds the first supported interface under sysRoot (default /sys)
// that has both a hidraw node and an ALSA card. devRoot (default /dev) is the
// directory the node paths are built from. busPath selects one device when
// several are plugged in; empty takes the first by bus path.
func Discover(sysRoot, devRoot, busPath string) (Info, error) {
	if sysRoot == "" {
		sysRoot = usbsys.DefaultRoot
	}
	if devRoot == "" {
		devRoot = "/dev"
	}
	devs, err := usbsys.FindFunc(sysRoot, Supported, busPath)
	if err != nil {
		return Info{}, err
	}
	var lastErr error = ErrNotFound
	for _, d := range devs {
		hid, cards := d.Hidraw(), d.Cards()
		if len(hid) == 0 {
			lastErr = fmt.Errorf("cm108: %s (%s) has no hidraw node: %w", d.Name, d.ID, ErrNotFound)
			continue
		}
		if len(cards) == 0 {
			lastErr = fmt.Errorf("cm108: %s (%s) has no ALSA card: %w", d.Name, d.ID, ErrNotFound)
			continue
		}
		chipName, _ := Identify(d.ID)
		return Info{
			BusPath: d.Name,
			ID:      d.ID,
			Chip:    chipName,
			Product: d.Product,
			Hidraw:  filepath.Join(devRoot, hid[0]),
			Card:    cards[0],
		}, nil
	}
	return Info{}, lastErr
}
