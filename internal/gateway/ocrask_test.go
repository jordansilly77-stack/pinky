package gateway

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// readerDev is a demo phone that reads text the way the Android driver
// does: a read with OCR adds its lines only when it asks for all the text
// (the tree looks complete), and it says whether this computer reads text
// (driver.TextReader). It keeps the options of every read.
type readerDev struct {
	*demo.Device
	reads bool // ReadsText
	mu    sync.Mutex
	lines []screen.Element
	opts  []driver.ObserveOptions
}

func (d *readerDev) ReadsText() bool { return d.reads }

func (d *readerDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, err := d.Device.Observe(ctx, opt)
	if err != nil {
		return s, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opts = append(d.opts, opt)
	if opt.OCR && opt.OCRAll && d.reads && (opt.MayCapture == nil || opt.MayCapture(s)) {
		s.Elements = append(s.Elements, d.lines...)
	}
	return s, nil
}

func (d *readerDev) text(label string, r screen.Rect) {
	d.mu.Lock()
	d.lines = append(d.lines, screen.Element{Role: screen.RoleText, Label: label, Bounds: r, Source: screen.SourceOCR})
	d.mu.Unlock()
}

// last returns the options of the latest read.
func (d *readerDev) last() driver.ObserveOptions {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opts[len(d.opts)-1]
}

func (d *readerDev) allReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, o := range d.opts {
		if o.OCRAll {
			n++
		}
	}
	return n
}

// phone_observe asks for the text the tree misses; with ocr, for all of
// it. Either way the driver may capture only screens Pinky would take a
// picture of.
func TestObserveAsksForText(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings")
	dev := &readerDev{Device: base, reads: true}
	dev.text("Wi-Fi 6 · 5 GHz", screen.Rect{X: 100, Y: 1500, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Settings")

	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if o := dev.last(); !o.OCR || o.OCRAll || o.MayCapture == nil || o.Frame == nil {
		t.Fatalf("phone_observe asked %+v", o)
	}
	if strings.Contains(res.Screen.Render(), "Wi-Fi 6") {
		t.Fatal("the text came without being asked for")
	}
	res = s.Observe(ctx, ObserveRequest{OCR: true})
	mustStatus(t, res, StatusSettled)
	o := dev.last()
	if !o.OCR || !o.OCRAll {
		t.Fatalf("phone_observe { ocr: true } asked %+v", o)
	}
	if !strings.Contains(res.Screen.Render(), `ocr "Wi-Fi 6 · 5 GHz"`) {
		t.Fatalf("no OCR line:\n%s", res.Screen.Render())
	}
	if strings.Contains(res.Message, "cannot read text") {
		t.Fatalf("message %q", res.Message)
	}

	// No pictures of a blocked app or of a locked phone, as for screenshots.
	wallet := &screen.Screen{App: screen.App{Name: "Google Wallet", ID: "com.google.android.apps.walletnfcrel"}, Width: 1080, Height: 2400}
	settings := &screen.Screen{App: screen.App{Name: "Settings", ID: "com.android.settings"}, Width: 1080, Height: 2400}
	locked := *settings
	locked.Locked = true
	unknown := &screen.Screen{Width: 1080, Height: 2400}
	if o.MayCapture(wallet) || o.MayCapture(&locked) || o.MayCapture(unknown) || !o.MayCapture(settings) {
		t.Fatal("MayCapture is wrong")
	}
}

// On a phone whose driver reads text, a tap on a spot the tree says little
// about is checked against the text there before any read has returned
// OCR text: "Pay" drawn in an image button asks.
func TestTextReaderChecksVagueTaps(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings") // nothing in the tree below y=772
	dev := &readerDev{Device: base, reads: true}
	dev.text("Pay ¥5,527.07", screen.Rect{X: 100, Y: 900, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Look at the bill")

	asked := make(chan NeedView, 1)
	go func() {
		n := waitNeed(t, g)
		asked <- n
		g.Decide(n.ID, Deny, "")
	}()
	res := s.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 930}})
	mustStatus(t, res, StatusDenied)
	if n := <-asked; n.Rule != "Payments" {
		t.Fatalf("asked under rule %q", n.Rule)
	}

	// A computer without OCR: no extra read, and phone_observe { ocr: true }
	// says why there is no text.
	plain := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	plain.Show("settings")
	none := &readerDev{Device: plain}
	none.text("Pay ¥5,527.07", screen.Rect{X: 100, Y: 900, W: 400, H: 60})
	g2 := newDevGateway(t, plain, none)
	s2, _ := connect(t, g2, "Look at the bill")
	res = s2.Act(ctx, Call{}, Action{Tool: "tap", Point: &screen.Point{X: 300, Y: 930}})
	mustStatus(t, res, StatusSettled)
	if n := none.allReads(); n != 0 {
		t.Fatalf("%d reads for all the text on a computer without OCR", n)
	}
	res = s2.Observe(ctx, ObserveRequest{OCR: true})
	if !strings.Contains(res.Message, "cannot read text from the screen's pixels") {
		t.Fatalf("message %q", res.Message)
	}
}

// phone_wait_for asks for all the text when the tree lacks it.
func TestWaitForAsksForAllText(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	base.Show("settings")
	dev := &readerDev{Device: base, reads: true}
	dev.text("Delivered", screen.Rect{X: 100, Y: 1500, W: 400, H: 60})
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Settings")
	res := s.WaitFor(ctx, WaitForRequest{Text: "Delivered", TimeoutMS: 2000})
	mustStatus(t, res, StatusSettled)
	if dev.allReads() == 0 {
		t.Fatal("no read asked for all the text")
	}
}
