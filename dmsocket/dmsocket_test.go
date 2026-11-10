package dmsocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"

	"github.com/stubbedev/lys-login/greeterconfig"
)

func TestRefusalsCrossTheWire(t *testing.T) {
	for r := range Refusal(len(refusalNames)) {
		data, err := json.Marshal(Answer("", fmt.Errorf("wrapped: %w", Refuse(r, "why"))))
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		var got Reply
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: decode %s: %v", r, data, err)
		}
		if got.Refusal != r || got.Error != "wrapped: why" || got.Path != "" {
			t.Errorf("%s crossed as %+v (%s)", r, got, data)
		}
	}
	if data, _ := json.Marshal(Answer("/etc/x", nil)); string(data) != `{"path":"/etc/x"}` {
		t.Errorf("a success = %s", data)
	}
	if r := Answer("", errors.New("disk full")); r.Refusal != Failed || r.Error != "disk full" {
		t.Errorf("a plain failure = %+v", r)
	}
	var r Reply
	if err := json.Unmarshal([]byte(`{"error":"x","refusal":"maybe"}`), &r); err == nil {
		t.Error("an unknown refusal decoded")
	}
	if _, err := Refusal(len(refusalNames)).MarshalText(); err == nil {
		t.Error("an out-of-range refusal encoded")
	}
}

func serveOnce(t *testing.T, e Endpoint[string], dir, answer string) {
	t.Helper()
	l, err := net.Listen("unix", e.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write([]byte(answer))
		_ = conn.Close()
	}()
}

func TestSendNamesTheRefusal(t *testing.T) {
	e := Endpoint[string]{Name: "refuse.sock", Exchange: 5 * time.Second}
	dir := sockdir.Dir(t)
	serveOnce(t, e, dir, `{"error":"dismissed by the person","refusal":"dismissed"}`)
	_, err := e.Send(dir, "x")
	refused, ok := errors.AsType[*RefusedError](err)
	if !ok || refused.Refusal != Dismissed || !strings.Contains(err.Error(), "dismissed by the person") {
		t.Fatalf("Send = %v, want the dismissal", err)
	}
}

func TestARefusalBeforeTheRequestIsReadStillReachesTheCaller(t *testing.T) {
	e := Endpoint[string]{Name: "refuse.sock", Exchange: 5 * time.Second}
	dir := sockdir.Dir(t)
	serveOnce(t, e, dir, `{"error":"only root may","refusal":"unauthorized"}`)
	_, err := e.Send(dir, strings.Repeat("x", 1<<22))
	if refused, ok := errors.AsType[*RefusedError](err); !ok || refused.Refusal != Unauthorized || !strings.Contains(err.Error(), "only root may") {
		t.Fatalf("Send = %v, want the refusal", err)
	}
}

func TestAFailedSendWithoutAnAnswerSaysSo(t *testing.T) {
	e := Endpoint[string]{Name: "silent.sock", Exchange: 5 * time.Second}
	dir := sockdir.Dir(t)
	serveOnce(t, e, dir, "")
	_, err := e.Send(dir, strings.Repeat("x", 1<<22))
	if err == nil || !strings.Contains(err.Error(), "send to") {
		t.Fatalf("Send = %v, want the failed send", err)
	}
}

func TestSendWithoutADaemon(t *testing.T) {
	if _, err := Apply.Send(sockdir.Dir(t), greeterconfig.Change{}); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("no daemon = %v, want ErrNoDaemon", err)
	}
}
