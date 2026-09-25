package libbox

import (
	"context"
	"reflect"
	"testing"

	"github.com/sagernet/sing-box/daemon"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// lx: SPEC 106 — CommandClient.SetEndpointEnabled forwards the request and
// returns the state as a field of an object (gomobile: no bare string returns).

type endpointToggleClient struct {
	daemon.StartedServiceClient
	request *daemon.SetEndpointEnabledRequest
	state   string
	err     error
}

func (c *endpointToggleClient) SetEndpointEnabled(ctx context.Context, request *daemon.SetEndpointEnabledRequest, opts ...grpc.CallOption) (*daemon.SetEndpointEnabledResponse, error) {
	c.request = request
	if c.err != nil {
		return nil, c.err
	}
	return &daemon.SetEndpointEnabledResponse{State: c.state}, nil
}

func TestCommandClientSetEndpointEnabled_LX(t *testing.T) {
	fake := &endpointToggleClient{state: "disabled"}
	client := NewCommandClient(nil, &CommandClientOptions{})
	client.grpcClient = fake
	result, err := client.SetEndpointEnabled("wg", false)
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.Tag != "wg" || fake.request.Enabled {
		t.Fatalf("request: %+v", fake.request)
	}
	if result.State != "disabled" {
		t.Fatalf("state = %q", result.State)
	}

	fake.err = status.Error(codes.NotFound, "endpoint not found: wg")
	result, err = client.SetEndpointEnabled("wg", true)
	if result != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("result %+v, err %v", result, err)
	}
	if !fake.request.Enabled {
		t.Fatalf("request: %+v", fake.request)
	}
}

// The result crosses gomobile: exported fields only, all bindable scalars.
func TestEndpointToggleResultShape_LX(t *testing.T) {
	resultType := reflect.TypeOf(EndpointToggleResult{})
	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i)
		if !field.IsExported() {
			t.Errorf("unexported field %s", field.Name)
		}
		switch field.Type.Kind() {
		case reflect.String, reflect.Bool, reflect.Int32, reflect.Int64:
		default:
			t.Errorf("field %s has non-bindable kind %s", field.Name, field.Type.Kind())
		}
	}
	method, ok := reflect.TypeOf(&CommandClient{}).MethodByName("SetEndpointEnabled")
	if !ok {
		t.Fatal("SetEndpointEnabled missing")
	}
	if method.Type.NumOut() != 2 || method.Type.Out(0) != reflect.TypeOf(&EndpointToggleResult{}) {
		t.Fatalf("signature: %s", method.Type)
	}
}
