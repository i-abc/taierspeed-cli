package defs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/briandowns/spinner"
	"github.com/go-ping/ping"
	log "github.com/sirupsen/logrus"
)

type ServerGlobal struct {
	ID   int    `json:"hostid,string"`
	Name string `json:"hostname"`
	IP   string `json:"hostip"`
	Port string `json:"port"`
	Prov string `json:"pname"`
	City string `json:"city"`
	Loc  string `json:"location,omitempty"`
	ISP  string `json:"oper,omitempty"`
}

func (s *ServerGlobal) GetISP() *ISPInfo {
	switch s.ISP {
	case "电信":
		return &TELECOM
	case "联通":
		return &UNICOM
	case "移动":
		return &MOBILE
	case "教育网":
		return &CERNET
	case "广电网":
		return &CATV
	case "鹏博士":
		return &DRPENG
	default:
		for _, isp := range ISPMap {
			if strings.HasSuffix(s.Name, isp.Name) {
				return isp
			}
		}
		return &DEFISP
	}
}

type ServerType uint8

const (
	GlobalSpeed ServerType = iota
	Perception
	WirelessSpeed
)

// Server represents a speed test server
type Server struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	IP          string     `json:"ip"`
	IPv6        string     `json:"ipv6"`
	Host        string     `json:"host"`
	Port        uint16     `json:"port"`
	Prov        uint8      `json:"province"`
	Province    string     `json:"-"`
	City        string     `json:"city"`
	ISP         uint8      `json:"isp"`
	DownloadURI string     `json:"download"`
	UploadURI   string     `json:"upload"`
	PingURI     string     `json:"ping"`
	Type        ServerType `json:"type"`
	NoICMP      bool       `json:"-"`
}

func (s *Server) DownloadURL() string {
	if s.DownloadURI != "" {
		return fmt.Sprintf("http://%s:%d%s", s.Host, s.Port, s.DownloadURI)
	} else {
		switch s.Type {
		case Perception:
			return fmt.Sprintf("http://%s:%d/speedtest/download", s.Host, s.Port)
		case WirelessSpeed:
			return fmt.Sprintf("http://%s:%d/GSpeedTestServer/download", s.Host, s.Port)
		default:
			return fmt.Sprintf("http://%s:%d/speed/File(1G).dl", s.Host, s.Port)
		}
	}
}

func (s *Server) UploadURL() string {
	if s.UploadURI != "" {
		return fmt.Sprintf("http://%s:%d%s", s.Host, s.Port, s.UploadURI)
	} else {
		switch s.Type {
		case Perception:
			return fmt.Sprintf("http://%s:%d/speedtest/upload", s.Host, s.Port)
		case WirelessSpeed:
			return fmt.Sprintf("http://%s:%d/GSpeedTestServer/upload", s.Host, s.Port)
		default:
			return fmt.Sprintf("http://%s:%d/speed/doAnalsLoad.do", s.Host, s.Port)
		}
	}
}

func (s *Server) PingURL() string {
	if s.PingURI != "" {
		return fmt.Sprintf("http://%s:%d%s", s.Host, s.Port, s.PingURI)
	} else {
		switch s.Type {
		case Perception:
			return fmt.Sprintf("http://%s:%d/speedtest/ping", s.Host, s.Port)
		case WirelessSpeed:
			return fmt.Sprintf("http://%s:%d/GSpeedTestServer/", s.Host, s.Port)
		default:
			return fmt.Sprintf("http://%s:%d/speed/", s.Host, s.Port)
		}
	}
}

// IsUp checks the speed test backend is up by accessing the ping URL
func (s *Server) IsUp() bool {
	req, err := http.NewRequest(http.MethodGet, s.PingURL(), nil)
	if err != nil {
		log.Debugf("Failed when creating HTTP request: %s", err)
		return false
	}

	req.Header.Set("User-Agent", AndroidUA)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Debugf("Error checking for server status: %s", err)
		return false
	}
	defer resp.Body.Close()

	// only return online if the ping URL returns nothing and 200
	return (resp.StatusCode == http.StatusOK) || (resp.StatusCode == http.StatusForbidden)
}

// ICMPPingAndJitter pings the server via ICMP echos and calculate the average ping and jitter
func (s *Server) ICMPPingAndJitter(count int, srcIp, network string) (float64, float64, error) {
	if s.NoICMP {
		log.Debugf("Skipping ICMP for server %s, will use HTTP ping", s.Name)
		return s.PingAndJitter(count + 2)
	}

	p, err := ping.NewPinger(s.Host)
	if err != nil {
		log.Debugf("ICMP ping failed: %s, will use HTTP ping", err)
		return s.PingAndJitter(count + 2)
	}
	p.SetPrivileged(true)
	p.SetNetwork(network)
	p.Count = count
	p.Timeout = time.Duration(count) * time.Second
	if srcIp != "" {
		p.Source = srcIp
	}
	if log.GetLevel() == log.DebugLevel {
		p.Debug = true
	}
	if err := p.Run(); err != nil {
		log.Debugf("Failed to ping target host: %s", err)
		log.Debug("Will try TCP ping")
		return s.PingAndJitter(count + 2)
	}

	stats := p.Statistics()

	var lastPing, jitter float64
	for idx, rtt := range stats.Rtts {
		if idx != 0 {
			instJitter := math.Abs(lastPing - float64(rtt.Milliseconds()))
			if idx > 1 {
				if jitter > instJitter {
					jitter = jitter*0.7 + instJitter*0.3
				} else {
					jitter = instJitter*0.2 + jitter*0.8
				}
			}
		}
		lastPing = float64(rtt.Milliseconds())
	}

	if len(stats.Rtts) == 0 {
		s.NoICMP = true
		log.Debugf("No ICMP pings returned for server %s (%s), trying TCP ping", s.Name, s.IP)
		return s.PingAndJitter(count + 2)
	}

	return float64(stats.AvgRtt.Milliseconds()), jitter, nil
}

// PingAndJitter pings the server via accessing ping URL and calculate the average ping and jitter
func (s *Server) PingAndJitter(count int) (float64, float64, error) {
	var pings []float64

	req, err := http.NewRequest(http.MethodGet, s.PingURL(), nil)
	if err != nil {
		log.Debugf("Failed when creating HTTP request: %s", err)
		return 0, 0, err
	}

	req.Header.Set("User-Agent", AndroidUA)

	for i := 0; i < count; i++ {
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Debugf("Failed when making HTTP request: %s", err)
			return 0, 0, err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		pings = append(pings, float64(time.Since(start).Milliseconds()))
	}

	// discard first result due to handshake overhead
	if len(pings) > 1 {
		pings = pings[1:]
	}

	var lastPing, jitter float64
	for idx, p := range pings {
		if idx != 0 {
			instJitter := math.Abs(lastPing - p)
			if idx > 1 {
				if jitter > instJitter {
					jitter = jitter*0.7 + instJitter*0.3
				} else {
					jitter = instJitter*0.2 + jitter*0.8
				}
			}
		}
		lastPing = p
	}

	return getAvg(pings), jitter, nil
}

// Download performs the actual download test
func (s *Server) Download(silent, useBytes, useMebi bool, requests int, duration time.Duration, token string) (float64, uint64, error) {
	counter := NewCounter()
	counter.SetMebi(useMebi)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	url := s.DownloadURL()
	if s.Type == GlobalSpeed {
		url = fmt.Sprintf("%s?key=%s", url, token)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		log.Debugf("Failed when creating HTTP request: %s", err)
		return 0, 0, err
	}

	req.Header.Set("User-Agent", BrowserUA)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "close")

	downloadDone := make(chan struct{}, requests)

	doDownload := func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !os.IsTimeout(err) {
			log.Debugf("Failed when making HTTP request: %s", err)
		} else {
			defer resp.Body.Close()

			if _, err = io.Copy(io.Discard, io.TeeReader(resp.Body, counter)); err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !os.IsTimeout(err) {
					log.Debugf("Failed when reading HTTP response: %s", err)
				}
			}

			downloadDone <- struct{}{}
		}
	}

	counter.Start()
	if !silent {
		pb := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
		pb.Prefix = "Downloading...  "
		pb.PostUpdate = func(s *spinner.Spinner) {
			if useBytes {
				s.Suffix = fmt.Sprintf("  %s", counter.AvgHumanize())
			} else {
				s.Suffix = fmt.Sprintf("  %.2f Mbps", counter.AvgMbps())
			}
		}

		pb.Start()
		defer func() {
			if useBytes {
				pb.FinalMSG = fmt.Sprintf("Download:\t%s\n (data used: %s)", counter.AvgHumanize(), counter.BytesHumanize())
			} else {
				pb.FinalMSG = fmt.Sprintf("Download:\t%.2f Mbps (data used: %.2f MB)\n", counter.AvgMbps(), counter.MBytes())
			}
			pb.Stop()
		}()
	}

	for i := 0; i < requests; i++ {
		go doDownload()
		time.Sleep(200 * time.Millisecond)
	}
	timeout := time.After(duration)
Loop:
	for {
		select {
		case <-timeout:
			ctx.Done()
			break Loop
		case <-downloadDone:
			go doDownload()
		}
	}

	return counter.AvgMbps(), counter.Total(), nil
}

// Upload performs the actual upload test
func (s *Server) Upload(noPrealloc, silent, useBytes, useMebi bool, requests, uploadSize int, duration time.Duration, token string) (float64, uint64, error) {
	counter := NewCounter()
	counter.SetMebi(useMebi)
	counter.SetUploadSize(uploadSize)

	if noPrealloc {
		log.Info("Pre-allocation is disabled, performance might be lower!")
		counter.reader = &SeekWrapper{rand.Reader}
	} else {
		counter.GenerateBlob()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.UploadURL(), counter)
	if err != nil {
		log.Debugf("Failed when creating HTTP request: %s", err)
		return 0, 0, err
	}

	req.Header.Set("User-Agent", AndroidUA)
	if s.Type != WirelessSpeed {
		req.Header.Set("Connection", "close")
		req.Header.Set("Charset", "UTF-8")
		req.Header.Set("Key", token)
		req.Header.Set("Content-Type", "multipart/form-data;boundary=00content0boundary00")
	} else {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	uploadDone := make(chan struct{}, requests)

	doUpload := func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !os.IsTimeout(err) {
			log.Debugf("Failed when making HTTP request: %s", err)
		} else if err == nil {
			defer resp.Body.Close()
			if _, err := io.Copy(io.Discard, resp.Body); err != nil {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !os.IsTimeout(err) {
					log.Debugf("Failed when reading HTTP response: %s", err)
				}
			}

			uploadDone <- struct{}{}
		}
	}

	counter.Start()
	if !silent {
		pb := spinner.New(spinner.CharSets[11], 100*time.Millisecond)
		pb.Prefix = "Uploading...  "
		pb.PostUpdate = func(s *spinner.Spinner) {
			if useBytes {
				s.Suffix = fmt.Sprintf("  %s", counter.AvgHumanize())
			} else {
				s.Suffix = fmt.Sprintf("  %.2f Mbps", counter.AvgMbps())
			}
		}

		pb.Start()
		defer func() {
			if useBytes {
				pb.FinalMSG = fmt.Sprintf("Upload:\t\t%s (data used: %s)\n", counter.AvgHumanize(), counter.BytesHumanize())
			} else {
				pb.FinalMSG = fmt.Sprintf("Upload:\t\t%.2f Mbps (data used: %.2f MB)\n", counter.AvgMbps(), counter.MBytes())
			}
			pb.Stop()
		}()
	}

	for i := 0; i < requests; i++ {
		go doUpload()
		time.Sleep(200 * time.Millisecond)
	}
	timeout := time.After(duration)
Loop:
	for {
		select {
		case <-timeout:
			ctx.Done()
			break Loop
		case <-uploadDone:
			go doUpload()
		}
	}

	return counter.AvgMbps(), counter.Total(), nil
}
