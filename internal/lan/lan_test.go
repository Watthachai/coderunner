package lan

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHostsAreTheAddressesCRNConnectsTo(t *testing.T) {
	got := Hosts("https://172.168.1.222:3443/crn/callback", "http://172.168.1.208:4000/v1", "", "http://router.test", "https://dv.test/x", "not a url\x7f")
	want := []string{"172.168.1.222:3443", "172.168.1.208:4000", "router.test:80", "dv.test:443"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("hosts %v, want %v", got, want)
	}
}

// macOS fails a launchd program's first connection to the local network while it checks
// the program's permission; Warm makes that first connection before any real call needs it.
func TestWarmSaysTheLocalNetworkIsOpenOnceAHostAnswers(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	var logs bytes.Buffer
	if !Warm(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), []string{closedAddr(t), l.Addr().String()}, 3, 10*time.Millisecond) {
		t.Fatalf("not open: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "local network open") {
		t.Errorf("logged %q", logs.String())
	}
}

func TestWarmSaysHowToOpenALocalNetworkThatStaysShut(t *testing.T) {
	var logs bytes.Buffer
	if Warm(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), []string{closedAddr(t)}, 2, 10*time.Millisecond) {
		t.Fatal("open")
	}
	for _, want := range []string{"level=WARN", "Local Network", "connection refused"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q: %s", want, logs.String())
		}
	}
}

func TestWarmWithNothingToReachSaysNothing(t *testing.T) {
	var logs bytes.Buffer
	if !Warm(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), nil, 3, time.Millisecond) || logs.Len() != 0 {
		t.Errorf("logged %q", logs.String())
	}
}

func closedAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	l.Close()
	return a
}

// A network that opens after a while is logged as not open yet, then open.
func TestWarmKeepsTryingUntilTheNetworkOpens(t *testing.T) {
	addr := closedAddr(t)
	var logs bytes.Buffer
	go func() {
		time.Sleep(60 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		t.Cleanup(func() { l.Close() })
	}()
	if !Warm(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)), []string{addr}, 50, 20*time.Millisecond) {
		t.Fatalf("not open: %s", logs.String())
	}
	for _, want := range []string{"local network not open yet", "local network open", "after="} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q: %s", want, logs.String())
		}
	}
}
