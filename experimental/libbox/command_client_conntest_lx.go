package libbox

import (
	"context"

	E "github.com/sagernet/sing/common/exceptions"
)

// StartConnectionTest runs an active connection test (SPEC 139) and streams its
// stage trace to the handler: every stage event goes to OnStage, the final result
// to OnResult, and only a transport failure of the stream to OnError. Cancelling
// the session is silent: no handler call follows it.
//
// The test is real traffic and wakes idle endpoints; run it only on an explicit
// user action.
func (c *CommandClient) StartConnectionTest(request *ConnectionTestRequest, handler ConnectionTestHandler) (*ConnectionTestSession, error) {
	if request == nil {
		return nil, E.New("start connection test: missing request")
	}
	client, parentCtx, err := c.getClientForCall()
	if err != nil {
		return nil, E.Cause(err, "start connection test")
	}

	streamCtx, cancel := context.WithCancel(parentCtx)
	session := &ConnectionTestSession{
		streamSession: streamSession{
			ctx:       streamCtx,
			cancel:    cancel,
			closeDone: make(chan struct{}),
		},
	}

	failStart := func(cause error, message string) (*ConnectionTestSession, error) {
		cancel()
		if c.standalone {
			c.closeConnection()
		}
		return nil, E.Cause(cause, message)
	}

	stream, err := client.StartConnectionTest(streamCtx, request.toGRPC())
	if err != nil {
		return failStart(err, "start connection test")
	}

	standalone := c.standalone
	go func() {
		defer func() {
			close(session.closeDone)
			if standalone {
				c.closeConnection()
			}
		}()
		for {
			event, recvErr := stream.Recv()
			if recvErr != nil {
				if session.ctx.Err() != nil {
					return
				}
				handler.OnError(E.Cause(recvErr, "connection test recv").Error())
				return
			}
			if stage := event.GetStage(); stage != nil {
				handler.OnStage(connectionTestStageFromGRPC(stage))
				continue
			}
			if result := event.GetResult(); result != nil {
				handler.OnResult(connectionTestResultFromGRPC(result))
				return
			}
		}
	}()

	return session, nil
}
