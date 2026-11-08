package ios

import (
	"context"
	"errors"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
)

// usbmuxd lists a phone over the network only once Wi-Fi connections are on
// for it: Finder's "Show this iPhone when on Wi-Fi", Xcode's "Connect via
// network", the phone's lockdown value EnableWifiConnections in the domain
// com.apple.mobile.wireless_lockdown. Nothing turns it on by itself, so a
// person who never ticked the box in Finder found Pinky's Wi-Fi switch did
// nothing. While Pinky uses phones over Wi-Fi, it turns the value on for a
// phone on a cable that trusts this computer, as Finder does when the box
// is ticked, and reads it first: a phone that has it on is left as it is.
// It never turns it off: turning Pinky's switch off, or removing the phone,
// leaves the phone's value alone (Finder turns it off). Once per attach;
// after a failure (a phone that did not answer in time) again after
// wifiAllowRetry.

const (
	wirelessDomain = "com.apple.mobile.wireless_lockdown"
	wirelessKey    = "EnableWifiConnections"
)

// wifiAllowRetry is how soon Pinky tries again to turn on Wi-Fi connections
// for a phone, on the same attach, after it failed.
const wifiAllowRetry = time.Minute

// wifiAllowed is what Pinky did about one phone's Wi-Fi connections, on one
// attach.
type wifiAllowed struct {
	key     entryKey
	done    bool // on, by Pinky or before
	running bool
	at      time.Time // the last try
}

// allowWiFi turns on Wi-Fi connections, in the background, for the phones
// List found on a cable that trust this computer (their lockdown values
// read: infos[i] has a name), while Pinky uses phones over Wi-Fi.
func (p *Provider) allowWiFi(entries []goios.DeviceEntry, infos []driver.Info, listed []bool) {
	for i, e := range entries {
		info := infos[i]
		if !listed[i] || e.Properties.ConnectionType != "USB" || info.Name == "" || info.State == driver.StateUnauthorized {
			continue
		}
		id, key := e.Properties.SerialNumber, keyOf(e)
		p.mu.Lock()
		a := p.wifi.allowed[id]
		skip := !p.wifi.on || p.wifi.forgot[id] ||
			a.key == key && (a.done || a.running || p.clock().Sub(a.at) < wifiAllowRetry)
		if !skip {
			if p.wifi.allowed == nil {
				p.wifi.allowed = map[string]wifiAllowed{}
			}
			p.wifi.allowed[id] = wifiAllowed{key: key, running: true, at: p.clock()}
			p.wifi.allowing.Add(1)
		}
		allow := p.wifi.allowFn
		p.mu.Unlock()
		if skip {
			continue
		}
		if allow == nil {
			allow = allowWiFiConnections
		}
		go func() {
			defer p.wifi.allowing.Done()
			turned, err := allow(context.Background(), e)
			p.mu.Lock()
			if a := p.wifi.allowed[id]; a.key == key {
				a.running, a.done = false, err == nil
				p.wifi.allowed[id] = a
			}
			p.mu.Unlock()
			switch {
			case err != nil:
				p.logger().Warn("iphone: could not turn on Wi-Fi connections", "device", id, "err", err)
			case turned:
				p.logger().Info("iphone: turned on Wi-Fi connections, as Finder's \"Show this iPhone when on Wi-Fi\" does", "device", id, "name", info.Name)
			}
		}()
	}
}

// errWiFiNotAllowed means the phone took the value but reads it as off.
var errWiFiNotAllowed = errors.New("the phone still reads Wi-Fi connections as off")

// allowWiFiConnections turns on Wi-Fi connections for the phone behind e
// unless they are on, in one lockdown session, and reports whether it
// turned them on.
func allowWiFiConnections(ctx context.Context, e goios.DeviceEntry) (bool, error) {
	return bounded(ctx, lockdownTimeout, func() (bool, error) {
		conn, err := goios.ConnectLockdownWithSession(e)
		if err != nil {
			return false, err
		}
		defer conn.Close()
		v, err := conn.GetValueForDomain(wirelessKey, wirelessDomain)
		if err != nil {
			return false, err
		}
		if on, _ := v.(bool); on {
			return false, nil
		}
		if err := conn.SetValueForDomain(wirelessKey, wirelessDomain, true); err != nil {
			return false, err
		}
		v, err = conn.GetValueForDomain(wirelessKey, wirelessDomain)
		if err != nil {
			return false, err
		}
		if on, _ := v.(bool); !on {
			return false, errWiFiNotAllowed
		}
		return true, nil
	})
}
