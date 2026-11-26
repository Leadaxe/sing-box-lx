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

// lx: SPEC 164 — the impair commands forward the request and return the
// status JSON as a field of an object (gomobile: no bare string returns).

type impairClient struct {
	daemon.StartedServiceClient
	set      *daemon.SetImpairRequest
	schedule *daemon.ScheduleImpairRequest
	get      *daemon.ImpairStatusRequest
	err      error
}

func (c *impairClient) answer() (*daemon.ImpairStatus, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &daemon.ImpairStatus{Json: `{"tag":"slow"}`}, nil
}

func (c *impairClient) SetImpair(ctx context.Context, request *daemon.SetImpairRequest, opts ...grpc.CallOption) (*daemon.ImpairStatus, error) {
	c.set = request
	return c.answer()
}

func (c *impairClient) ScheduleImpair(ctx context.Context, request *daemon.ScheduleImpairRequest, opts ...grpc.CallOption) (*daemon.ImpairStatus, error) {
	c.schedule = request
	return c.answer()
}

func (c *impairClient) GetImpairStatus(ctx context.Context, request *daemon.ImpairStatusRequest, opts ...grpc.CallOption) (*daemon.ImpairStatus, error) {
	c.get = request
	return c.answer()
}

func TestCommandClientImpair_LX(t *testing.T) {
	fake := &impairClient{}
	client := NewCommandClient(nil, &CommandClientOptions{})
	client.grpcClient = fake

	result, err := client.SetImpair("slow", `{"link_params":{"outage":true}}`)
	if err != nil || result.Content != `{"tag":"slow"}` || fake.set.OutboundTag != "slow" || fake.set.Entry != `{"link_params":{"outage":true}}` {
		t.Fatalf("set: %+v %v %+v", result, err, fake.set)
	}
	result, err = client.ScheduleImpair("slow", `[]`, "first_use", "60s")
	if err != nil || result.Content != `{"tag":"slow"}` || fake.schedule.Entries != `[]` || fake.schedule.Start != "first_use" || fake.schedule.Repeat != "60s" {
		t.Fatalf("schedule: %+v %v %+v", result, err, fake.schedule)
	}
	result, err = client.GetImpairStatus("slow")
	if err != nil || result.Content != `{"tag":"slow"}` || fake.get.OutboundTag != "slow" {
		t.Fatalf("status: %+v %v", result, err)
	}

	fake.err = status.Error(codes.InvalidArgument, "not an impair outbound: proxy")
	if result, err = client.GetImpairStatus("proxy"); result != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error: %+v %v", result, err)
	}
}

// Every impair method returns *ImpairStatus, whose only field is a string:
// nothing returns a bare string across gomobile.
func TestImpairShape_LX(t *testing.T) {
	statusType := reflect.TypeOf(ImpairStatus{})
	if statusType.NumField() != 1 || statusType.Field(0).Name != "Content" || statusType.Field(0).Type.Kind() != reflect.String {
		t.Fatalf("ImpairStatus fields: %v", statusType)
	}
	for _, name := range []string{"SetImpair", "ScheduleImpair", "GetImpairStatus"} {
		method, ok := reflect.TypeOf(&CommandClient{}).MethodByName(name)
		if !ok {
			t.Fatal(name, " missing")
		}
		if method.Type.NumOut() != 2 || method.Type.Out(0) != reflect.TypeOf(&ImpairStatus{}) {
			t.Fatalf("%s signature: %s", name, method.Type)
		}
		for i := 1; i < method.Type.NumIn(); i++ {
			if method.Type.In(i).Kind() != reflect.String {
				t.Fatalf("%s argument %d is %s", name, i, method.Type.In(i))
			}
		}
	}
}
