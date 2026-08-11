//go:build with_lx_command

package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// lx: SPECS/TASKS/058-GET_URL_VIA_OUTBOUND
//
// Стенд: httptest-сервер на локальном сокете + фиктивный узел, который дайлит
// напрямую. Он проверяет контракт handler'а (резолв тега, кламп, Variant B,
// truncated, нетронутая история), а не транспорт — путь через реальный
// vless/WG проверяется на устройстве.

// probeDialer — минимальный N.Dialer поверх net.Dialer. Считает дайлы, чтобы
// тест мог убедиться: обмен действительно пошёл через переданный узел.
type probeDialer struct {
	dials int
	fail  error
	block chan struct{} // если не nil — дайл висит до закрытия канала или отмены ctx
}

func (d *probeDialer) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	d.dials++
	if d.fail != nil {
		return nil, d.fail
	}
	if d.block != nil {
		select {
		case <-d.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return (&net.Dialer{}).DialContext(ctx, network, destination.String())
}

func (d *probeDialer) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	return nil, N.ErrUnknownNetwork
}

// probeOutbound оборачивает дайлер в adapter.Outbound — форма, в которой узел
// лежит в менеджере. Методы сверх N.Dialer handler'у не нужны. Дайлер держится
// полем, а не встраиванием: встроенный вместе с adapter.Outbound он даёт
// неоднозначный DialContext на одной глубине.
type probeOutbound struct {
	adapter.Outbound
	dialer *probeDialer
	tag    string
}

func (o *probeOutbound) Tag() string  { return o.tag }
func (o *probeOutbound) Type() string { return "probe" }

func (o *probeOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return o.dialer.DialContext(ctx, network, destination)
}

// probeEndpoint — то же самое в форме adapter.Endpoint (WG/AWG-узел).
type probeEndpoint struct {
	adapter.Endpoint
	dialer *probeDialer
	tag    string
}

func (e *probeEndpoint) Tag() string  { return e.tag }
func (e *probeEndpoint) Type() string { return "probe-endpoint" }

func (e *probeEndpoint) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return e.dialer.DialContext(ctx, network, destination)
}

// Менеджеры: реализован только тот метод, который зовёт handler. Любой другой
// вызов упадёт на nil-интерфейсе — это осознанная страховка от расширения
// handler'а без пересмотра теста.
type probeOutboundManager struct {
	adapter.OutboundManager
	byTag map[string]adapter.Outbound
}

func (m *probeOutboundManager) Outbound(tag string) (adapter.Outbound, bool) {
	outbound, loaded := m.byTag[tag]
	return outbound, loaded
}

type probeEndpointManager struct {
	adapter.EndpointManager
	byTag map[string]adapter.Endpoint
}

func (m *probeEndpointManager) Get(tag string) (adapter.Endpoint, bool) {
	endpoint, loaded := m.byTag[tag]
	return endpoint, loaded
}

// newProbeService собирает STARTED-сервис с одним outbound-узлом и одним
// endpoint-узлом, разделяющими дайлер.
func newProbeService(dialer *probeDialer) *StartedService {
	return &StartedService{
		serviceStatus: &ServiceStatus{Status: ServiceStatus_STARTED},
		instance: &Instance{
			ctx:                   context.Background(),
			urlTestHistoryStorage: urltest.NewHistoryStorage(),
			outboundManager: &probeOutboundManager{byTag: map[string]adapter.Outbound{
				"node": &probeOutbound{dialer: dialer, tag: "node"},
			}},
			endpointManager: &probeEndpointManager{byTag: map[string]adapter.Endpoint{
				"wg": &probeEndpoint{dialer: dialer, tag: "wg"},
			}},
		},
	}
}

// Тег резолвится в ОБОИХ менеджерах: и обычный outbound, и endpoint (WG/AWG)
// проверяются одним вызовом. Регрессия здесь означала бы «WG-узлы не
// продиагностировать» — ровно то, ради чего у донора резолв двойной.
func TestGetURLViaOutbound_ResolvesOutboundAndEndpoint_LX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		writer.Write([]byte("ip=1.2.3.4\nwarp=off\n"))
	}))
	defer server.Close()

	for _, tag := range []string{"node", "wg"} {
		t.Run(tag, func(t *testing.T) {
			dialer := &probeDialer{}
			service := newProbeService(dialer)

			response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
				OutboundTag: tag,
				Link:        server.URL,
			})
			if err != nil {
				t.Fatalf("transport error: %v", err)
			}
			if response.Error != "" {
				t.Fatalf("payload error: %s", response.Error)
			}
			if dialer.dials == 0 {
				t.Fatal("exchange did not go through the node's dialer")
			}
			if response.HttpStatus != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.HttpStatus)
			}
			if !strings.Contains(string(response.Body), "warp=off") {
				t.Fatalf("body = %q, want the trace payload", response.Body)
			}
			if response.ContentType != "text/plain" {
				t.Fatalf("contentType = %q, want text/plain", response.ContentType)
			}
			if response.RemoteAddr == "" {
				t.Fatal("remoteAddr must carry the address reached from inside the tunnel")
			}
			if response.Truncated {
				t.Fatal("a short body must not be reported as truncated")
			}
		})
	}
}

// Не-2xx — РЕЗУЛЬТАТ, а не ошибка: 403 от Cloudflare это говорящий ответ, ради
// которого пробник и существует. Регрессия сюда вернула бы «узел не отвечает»
// вместо «узел отвечает отказом».
func TestGetURLViaOutbound_NonOKIsResult_LX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte(`{"error":"denied"}`))
	}))
	defer server.Close()

	service := newProbeService(&probeDialer{})
	response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
		OutboundTag: "node",
		Link:        server.URL,
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if response.Error != "" {
		t.Fatalf("403 must not be reported as an error, got %q", response.Error)
	}
	if response.HttpStatus != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.HttpStatus)
	}
	if !strings.Contains(string(response.Body), "denied") {
		t.Fatalf("body of a non-2xx response must be returned, got %q", response.Body)
	}
}

// Кламп читается в ядре, а не у клиента: 0 → дефолт, выше потолка → потолок,
// тело длиннее лимита → усечено ровно по лимиту с явным признаком.
func TestGetURLViaOutbound_ClampsAndTruncates_LX(t *testing.T) {
	const bodySize = getURLDefaultMaxBytes + 4096
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write([]byte(strings.Repeat("x", bodySize)))
	}))
	defer server.Close()

	for _, testCase := range []struct {
		name     string
		maxBytes uint32
		wantLen  int
		wantCut  bool
	}{
		{"zero means default", 0, getURLDefaultMaxBytes, true},
		{"explicit small limit", 16, 16, true},
		{"above ceiling clamps to ceiling", getURLMaxBytesCeiling * 4, bodySize, false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service := newProbeService(&probeDialer{})
			response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
				OutboundTag: "node",
				Link:        server.URL,
				MaxBytes:    testCase.maxBytes,
			})
			if err != nil {
				t.Fatalf("transport error: %v", err)
			}
			if response.Error != "" {
				t.Fatalf("payload error: %s", response.Error)
			}
			if len(response.Body) != testCase.wantLen {
				t.Fatalf("body length = %d, want %d", len(response.Body), testCase.wantLen)
			}
			if response.Truncated != testCase.wantCut {
				t.Fatalf("truncated = %v, want %v", response.Truncated, testCase.wantCut)
			}
		})
	}
}

// Граничный случай усечения: тело ровно в лимит — не обрезано. Именно ради
// него handler читает на байт больше лимита.
func TestGetURLViaOutbound_ExactLimitIsNotTruncated_LX(t *testing.T) {
	const limit = 512
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write([]byte(strings.Repeat("y", limit)))
	}))
	defer server.Close()

	service := newProbeService(&probeDialer{})
	response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
		OutboundTag: "node",
		Link:        server.URL,
		MaxBytes:    limit,
	})
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if response.Truncated {
		t.Fatal("a body exactly at the limit must not be reported as truncated")
	}
	if len(response.Body) != limit {
		t.Fatalf("body length = %d, want %d", len(response.Body), limit)
	}
}

// Variant B: прикладной исход — в payload, handler всегда возвращает (resp, nil).
func TestGetURLViaOutbound_VariantB_LX(t *testing.T) {
	t.Run("unknown tag", func(t *testing.T) {
		service := newProbeService(&probeDialer{})
		response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
			OutboundTag: "missing",
			Link:        "http://example.invalid",
		})
		if err != nil {
			t.Fatalf("unknown tag must not be a transport error, got %v", err)
		}
		if !strings.Contains(response.Error, "not found") {
			t.Fatalf("error = %q, want a not-found payload error", response.Error)
		}
	})

	t.Run("dial failure", func(t *testing.T) {
		service := newProbeService(&probeDialer{fail: net.ErrClosed})
		response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
			OutboundTag: "node",
			Link:        "http://192.0.2.1:80",
		})
		if err != nil {
			t.Fatalf("dial failure must not be a transport error, got %v", err)
		}
		if response.Error == "" {
			t.Fatal("a failed dial must be reported in the payload")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		service := newProbeService(&probeDialer{block: make(chan struct{})})
		started := time.Now()
		response, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
			OutboundTag: "node",
			Link:        "http://192.0.2.1:80",
			Timeout:     150,
		})
		if err != nil {
			t.Fatalf("timeout must not be a transport error, got %v", err)
		}
		if response.Error == "" {
			t.Fatal("a timed-out fetch must be reported in the payload")
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("timeout was not honoured: took %v", elapsed)
		}
	})
}

// Не-STARTED — транспортная ошибка, как у донора: это состояние сервиса,
// а не исход обмена.
func TestGetURLViaOutbound_NotStarted_LX(t *testing.T) {
	service := &StartedService{serviceStatus: &ServiceStatus{Status: ServiceStatus_IDLE}}
	if _, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
		OutboundTag: "node",
		Link:        "http://example.invalid",
	}); err == nil {
		t.Fatal("a non-STARTED service must fail the call")
	}
}

// Отмена вызова обрывает висящий фетч на дайле — та же модель, что у
// URLTestOutbound: клиент, закрывший запрос, останавливает работу.
func TestGetURLViaOutbound_CancelAbortsFetch_LX(t *testing.T) {
	service := newProbeService(&probeDialer{block: make(chan struct{})})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan *GetURLViaOutboundResponse, 1)
	go func() {
		response, _ := service.GetURLViaOutbound(ctx, &GetURLViaOutboundRequest{
			OutboundTag: "node",
			Link:        "http://192.0.2.1:80",
		})
		done <- response
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case response := <-done:
		if response == nil || response.Error == "" {
			t.Fatal("a cancelled fetch must return a payload error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the call did not abort the fetch")
	}
}

// История urltest после фетча не меняется. Фетч — не замер: его время включает
// чтение тела по произвольному URL, и запись такого числа испортила бы
// показания задержек узла в UI.
func TestGetURLViaOutbound_LeavesURLTestHistoryIntact_LX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write([]byte("ok"))
	}))
	defer server.Close()

	service := newProbeService(&probeDialer{})
	storage := service.instance.urlTestHistoryStorage
	storage.StoreURLTestHistory("node", &adapter.URLTestHistory{Time: time.Now(), Delay: 42})

	if _, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
		OutboundTag: "node",
		Link:        server.URL,
	}); err != nil {
		t.Fatalf("transport error: %v", err)
	}

	history := storage.LoadURLTestHistory("node")
	if history == nil {
		t.Fatal("the probe deleted the node's urltest history")
	}
	if history.Delay != 42 {
		t.Fatalf("delay = %d, want the untouched 42 — the probe must not write measurements", history.Delay)
	}
}

// Заголовок Host перекладывается в request.Host: в net/http это отдельное поле,
// установка через Header.Set игнорируется молча.
func TestGetURLViaOutbound_HostHeaderOverridesHost_LX(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen <- request.Host
		writer.Write([]byte("ok"))
	}))
	defer server.Close()

	service := newProbeService(&probeDialer{})
	if _, err := service.GetURLViaOutbound(context.Background(), &GetURLViaOutboundRequest{
		OutboundTag: "node",
		Link:        server.URL,
		Headers: []*HttpHeaderPair{
			{Key: "Host", Value: "example.com"},
			{Key: "X-Probe", Value: "1"},
		},
	}); err != nil {
		t.Fatalf("transport error: %v", err)
	}

	select {
	case host := <-seen:
		if host != "example.com" {
			t.Fatalf("Host = %q, want example.com", host)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the request")
	}
}
