package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"
)

const viewerLease = 25 * time.Second

type viewer struct {
	pc      *webrtc.PeerConnection
	cancel  context.CancelFunc
	expires time.Time
}
type videoServer struct {
	gate    inputGate
	api     *webrtc.API
	socket  *net.UDPConn
	mu      sync.Mutex
	viewers map[string]*viewer
	cancel  context.CancelFunc
	closed  bool
}

func newVideoServer(port int, address string) (*videoServer, error) {
	settings := webrtc.SettingEngine{}
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	if address != "" {
		if net.ParseIP(address).To4() == nil {
			return nil, errors.New("BQBOT_ICE_HOST must be a reachable IPv4 address")
		}
		err := settings.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{External: []string{address}, AsCandidateType: webrtc.ICECandidateTypeHost, Mode: webrtc.ICEAddressRewriteAppend})
		if err != nil {
			return nil, err
		}
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, err
	}
	settings.SetICEUDPMux(webrtc.NewICEUDPMux(nil, conn))
	ctx, cancel := context.WithCancel(context.Background())
	s := &videoServer{api: webrtc.NewAPI(webrtc.WithSettingEngine(settings)), socket: conn, viewers: map[string]*viewer{}, cancel: cancel}
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				s.mu.Lock()
				var expired []*viewer
				for id, v := range s.viewers {
					if !now.Before(v.expires) {
						delete(s.viewers, id)
						expired = append(expired, v)
					}
				}
				s.mu.Unlock()
				for _, v := range expired {
					v.cancel()
					v.pc.Close()
				}
			}
		}
	}()
	return s, nil
}

func (s *videoServer) remove(id string) {
	s.mu.Lock()
	v := s.viewers[id]
	delete(s.viewers, id)
	s.mu.Unlock()
	if v != nil {
		v.cancel()
		v.pc.Close()
	}
}

func (s *videoServer) close() {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	active := s.viewers
	s.viewers = map[string]*viewer{}
	s.mu.Unlock()
	for _, v := range active {
		v.cancel()
		v.pc.Close()
	}
	s.socket.Close()
}

func (s *videoServer) offer(w http.ResponseWriter, r *http.Request) {
	var offer struct {
		SDP     string `json:"sdp"`
		Control bool   `json:"control"`
	}
	if !readJSON(w, r, &offer) {
		return
	}
	if offer.SDP == "" {
		http.Error(w, "missing offer", 400)
		return
	}
	pc, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, "cannot start peer", 500)
		return
	}
	var key [16]byte
	rand.Read(key[:])
	id := hex.EncodeToString(key[:])
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.closed || len(s.viewers) >= 4 {
		s.mu.Unlock()
		cancel()
		pc.Close()
		http.Error(w, "viewer limit reached", 429)
		return
	}
	s.viewers[id] = &viewer{pc: pc, cancel: cancel, expires: time.Now().Add(viewerLease)}
	s.mu.Unlock()
	success := false
	defer func() {
		if !success {
			s.remove(id)
		}
	}()
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "desktop", "workspace")
	if err != nil {
		http.Error(w, "cannot create video track", 500)
		return
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		http.Error(w, "cannot attach track", 500)
		return
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if offer.Control {
			s.attachInput(ctx, dc)
		} else {
			dc.Close()
		}
	})
	var once sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			once.Do(func() { go func() { _ = captureVP8(ctx, track); s.remove(id) }() })
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			go s.remove(id)
		}
	})
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		http.Error(w, "invalid WebRTC offer", 400)
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, "cannot negotiate video", 400)
		return
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if pc.SetLocalDescription(answer) != nil {
		http.Error(w, "invalid local description", 500)
		return
	}
	select {
	case <-gather:
	case <-r.Context().Done():
		return
	case <-time.After(7 * time.Second):
		http.Error(w, "ICE gathering timed out", 504)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	success = json.NewEncoder(w).Encode(map[string]any{"id": id, "sdp": pc.LocalDescription().SDP, "control": offer.Control}) == nil
}

func (s *videoServer) renew(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	v := s.viewers[r.PathValue("id")]
	if v != nil {
		v.expires = time.Now().Add(viewerLease)
	}
	s.mu.Unlock()
	if v == nil {
		http.Error(w, "viewer expired", 404)
		return
	}
	w.WriteHeader(204)
}
func (s *videoServer) stop(w http.ResponseWriter, r *http.Request) {
	s.remove(r.PathValue("id"))
	w.WriteHeader(204)
}

func captureVP8(ctx context.Context, track *webrtc.TrackLocalStaticSample) error {
	ctx, cancel := context.WithCancel(ctx)
	const rate = 12
	// Query the live root window rather than capturing a fixed rectangle.
	size, err := exec.CommandContext(ctx, "xdotool", "getdisplaygeometry").Output()
	if err != nil {
		cancel()
		return err
	}
	var width, height int
	if _, err = fmt.Sscan(string(size), &width, &height); err != nil || width < 1 || height < 1 {
		cancel()
		return errors.New("invalid X11 desktop size")
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "x11grab", "-framerate", strconv.Itoa(rate), "-video_size", fmt.Sprintf("%dx%d", width, height), "-i", os.Getenv("DISPLAY"), "-an", "-c:v", "libvpx", "-pix_fmt", "yuv420p", "-deadline", "realtime", "-cpu-used", "8", "-threads", "2", "-lag-in-frames", "0", "-g", "12", "-b:v", "1200k", "-flush_packets", "1", "-f", "ivf", "pipe:1")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		return err
	}
	defer func() { cancel(); out.Close(); cmd.Wait() }()
	frames, _, err := ivfreader.NewWith(out)
	if err != nil {
		return fmt.Errorf("start screen capture: %w", err)
	}
	for {
		data, _, err := frames.ParseNextFrame()
		if err != nil {
			return err
		}
		if err = track.WriteSample(media.Sample{Data: data, Duration: time.Second / rate}); err != nil {
			return err
		}
	}
}
