package libbox

import (
	"strconv"
	"sync"
	"testing"
)

// TestConnectionsConcurrentAccess reproduces the SPEC 016 race: one goroutine drives
// ApplyEvents (writes connectionMap) while others read via Iterator/FilterState/SortBy.
// Before the mutex this tripped Go's "concurrent map iteration and map write" fatal error
// (unrecoverable). Run with -race; it must complete cleanly.
func TestConnectionsConcurrentAccess(t *testing.T) {
	connections := NewConnections()

	const rounds = 2000
	var wg sync.WaitGroup

	// Writer: mirrors a CommandConnections subscriber goroutine applying a stream of events.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			id := strconv.Itoa(i % 64) // churn a bounded id set so the map both grows and updates
			connections.ApplyEvents(&ConnectionEvents{
				events: []*ConnectionEvent{{
					Type:       ConnectionEventNew,
					ID:         id,
					Connection: &Connection{ID: id, CreatedAt: int64(i)},
				}},
			})
		}
	}()

	// Readers: the UI side — iterate, filter, and sort concurrently with the writer.
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				it := connections.Iterator()
				for it.HasNext() {
					_ = it.Next()
				}
				connections.FilterState(ConnectionStateActive)
				connections.SortByDate()
			}
		}()
	}

	wg.Wait()
}
