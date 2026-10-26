//go:build with_lx_command

package v2rayxhttp

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/common/lxtrace"
	M "github.com/sagernet/sing/common/metadata"
)

// lx: SPEC 139 — the `transport` stage of an XHTTP dial: mode, method, path
// and the HTTP status of the download response; a refused download is a failed
// `transport_response` child and the root cause.

type statusTransport struct {
	status int
}

func (t *statusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		io.Copy(io.Discard, request.Body)
		request.Body.Close()
	}
	return &http.Response{
		StatusCode: t.status,
		Status:     http.StatusText(t.status),
		Body:       io.NopCloser(strings.NewReader("")),
	}, nil
}

func TestTransportStage_LX(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		status int
	}{
		{"ok", http.StatusOK},
		{"refused", http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			meta, err := normalizeMeta(metaOptions{ScMinPostsIntervalMs: "0"}, modePacketUp)
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{
				ctx:         context.Background(),
				scheme:      "https",
				host:        "example.com",
				serverAddr:  M.ParseSocksaddr("example.com:443"),
				path:        "/xhttp",
				mode:        modeAuto, // no REALITY → packet-up
				meta:        meta,
				httpVersion: httpVersion2,
				xmux:        singleTransportXmux(&statusTransport{status: testCase.status}),
			}
			var (
				emitted   []lxtrace.StageSnapshot
				emitMutex sync.Mutex
			)
			trace := lxtrace.New(lxtrace.Options{Emit: func(snapshot lxtrace.StageSnapshot) {
				emitMutex.Lock()
				emitted = append(emitted, snapshot)
				emitMutex.Unlock()
			}})
			conn, err := client.DialContext(lxtrace.With(context.Background(), trace))
			if err != nil {
				t.Fatal(err)
			}
			// The download response is processed asynchronously; a Read
			// synchronises on it (EOF or the refusal).
			conn.Read(make([]byte, 1))
			conn.Close()

			summary := trace.Finish(nil)
			var stage, child *lxtrace.StageSnapshot
			for i := range summary.Stages {
				switch summary.Stages[i].Kind {
				case "transport":
					stage = &summary.Stages[i]
				case "transport_response":
					child = &summary.Stages[i]
				}
			}
			if stage == nil {
				t.Fatalf("no transport stage: %+v", summary.Stages)
			}
			want := map[string]string{
				"mode":          modePacketUp,
				"method":        http.MethodGet,
				"upload_method": http.MethodPost,
				"path":          "/xhttp",
				"http_version":  "2",
				"http_status":   strconv.Itoa(testCase.status),
			}
			for key, value := range want {
				if got := attrOf(*stage, key); got != value {
					t.Fatalf("attr %s = %q, want %q (%+v)", key, got, value, stage.Attrs)
				}
			}
			if stage.Subject != "xhttp" || stage.Result != lxtrace.OK {
				t.Fatalf("the dial itself succeeds: %+v", *stage)
			}
			if testCase.status == http.StatusOK {
				if child != nil || summary.RootCause != 0 {
					t.Fatalf("no failure expected: %+v", summary)
				}
			} else {
				if child == nil || child.Result != lxtrace.Failed || child.Parent != stage.ID {
					t.Fatalf("a refused download is a failed child of the transport stage: %+v", summary.Stages)
				}
				if summary.RootCause != child.ID {
					t.Fatalf("root cause = %d, want the refused response %d", summary.RootCause, child.ID)
				}
			}
			emitMutex.Lock()
			defer emitMutex.Unlock()
			if len(emitted) == 0 || emitted[0].Kind != "transport" || emitted[0].Result != lxtrace.Running {
				t.Fatalf("the stage is emitted when it begins: %+v", emitted)
			}
		})
	}
}

func attrOf(stage lxtrace.StageSnapshot, key string) string {
	for _, attr := range stage.Attrs {
		if attr.Key == key {
			return attr.Value
		}
	}
	return ""
}
