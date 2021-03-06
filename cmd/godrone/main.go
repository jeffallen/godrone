package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/felixge/godrone"
	"github.com/gorilla/websocket"
)

var verbose = flag.Int("verbose", 0, "verbosity: 1=some 2=lots")
var addr = flag.String("addr", ":80", "Address to listen on (default is \":80\")")
var dummy = flag.Bool("dummy", false, "Dummy drone: do not open navboard/motorboard.")

func main() {
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("Godrone started")

	var firmware *godrone.Firmware
	if *dummy {
		firmware, _ = godrone.NewCustomFirmware(&mockNavboard{}, &mockMotorboard{})
	} else {
		var err error
		firmware, err = godrone.NewFirmware()
		if err != nil {
			log.Fatalf("%s", err)
		}
		defer firmware.Close()
	}

	reqCh := make(chan Request)
	go serveHttp(reqCh)

	calibrate := func() {
		for {
			// @TODO The LEDs don't seem to turn off when this is called again after
			// a calibration errors, instead they just blink. Not sure why.
			firmware.Motorboard.WriteLeds(godrone.Leds(godrone.LedOff))
			log.Printf("Calibrating sensors")
			err := firmware.Calibrate()
			if err != nil {
				firmware.Motorboard.WriteLeds(godrone.Leds(godrone.LedRed))
				time.Sleep(time.Second)
			} else {
				log.Printf("Finished calibration")
				firmware.Motorboard.WriteLeds(godrone.Leds(godrone.LedGreen))
				break
			}
		}
	}
	calibrate()
	for {
		select {
		case req := <-reqCh:
			var res Response
			res.Actual = firmware.Actual
			res.Desired = firmware.Desired
			res.Time = time.Now()
			if req.SetDesired != nil {
				// Log changes to desired
				if Verbose() && firmware.Desired != *req.SetDesired {
					log.Print("New desired attitude:", firmware.Desired)
				}
				firmware.Desired = *req.SetDesired
			}
			if req.Calibrate {
				calibrate()
			}
			req.Response <- res
			if reallyVerbose() {
				log.Print("Request: ", req, "Response: ", res)
			}
		default:
		}
		var err error
		if firmware.Desired.Altitude > 0 {
			err = firmware.Control()
		} else {
			// Something subtle to note here: When the motors are running,
			// but then desired altitude goes to zero (for instance due to
			// the emergency command in the UI) we end up here.
			//
			// We never actually send a motors=0 command. Instead we count on
			// the failsafe behavior of the motorboard, which puts the motors to
			// zero if it does not receive a new motor command soon enough.
			err = firmware.Observe()
		}
		if err != nil {
			log.Printf("%s", err)
		}
	}
}

type Request struct {
	SetDesired *godrone.Placement
	Calibrate  bool
	Response   chan Response
}

type Response struct {
	Time    time.Time         `json:"time"`
	Actual  godrone.Placement `json:"actual,omitempty"`
	Desired godrone.Placement `json:"desired,omitempty"`
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func serveHttp(reqCh chan<- Request) {
	err := http.ListenAndServe(*addr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first := true
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Failed to upgrade ws: %s", err)
			return
		}
		defer conn.Close()
		for {
			var req Request
			if err := conn.ReadJSON(&req); err != nil {
				log.Printf("Failed to read from ws: %s", err)
				return
			}

			// On the first request from the websocket, we ignore what they
			// asked for. This way, they find out our current altitude, and
			// can sync up to us, thereby not causing a crash on reconnect.
			if first {
				req.SetDesired = nil
				first = false
			}

			req.Response = make(chan Response)
			reqCh <- req
			res := <-req.Response
			if err := conn.WriteJSON(res); err != nil {
				log.Printf("Failed to write to ws: %s", err)
				return
			}
		}
	}))
	if err != nil {
		log.Fatalf("Failed to serve http: %s", err)
	}
}

type Client struct{}

func reallyVerbose() bool { return *verbose > 1 }
func Verbose() bool       { return *verbose > 0 }

// A MockNavboard is a NavBoardReader that does not talk to real hardware.
type mockNavboard struct {
	seq uint16
}

func (b *mockNavboard) Read() (data godrone.Navdata, err error) {
	b.seq++
	return godrone.Navdata{
		Seq:      b.seq,
		AccRoll:  2000,
		AccPitch: 2000,
		AccYaw:   8000,
	}, nil
}

// A MockMotorboard is a MotorLedWriter that does not talk to real hardware.
type mockMotorboard struct {
	speeds [4]float64
}

func (m *mockMotorboard) WriteLeds(leds [4]godrone.LedColor) error {
	log.Print("Mock WriteLeds: ", leds)
	return nil
}

func (m *mockMotorboard) WriteSpeeds(speeds [4]float64) error {
	if m.speeds != speeds {
		log.Print("Mock WriteSpeeds: ", speeds)
	}
	m.speeds = speeds
	return nil
}
