//go:build windows

package walk

import (
	"testing"
	"testing/synctest"
)

func TestLayoutTreeCancellationDuringResultDelivery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := &boxLayoutItem{}
		root.ctx = &LayoutContext{layoutItem2MinSizeEffective: make(map[LayoutItem]Size), dpi: 96}
		cancel := make(chan struct{})
		done := make(chan []LayoutResult)
		exited := make(chan struct{})
		go func() {
			layoutTree(root, Size{100, 100}, cancel, done, nil)
			close(exited)
		}()

		// Wait until layout has finished and its result sender is blocked:
		// the coordinator is no longer consuming results during shutdown.
		synctest.Wait()
		close(cancel)
		synctest.Wait()
		select {
		case <-exited:
		default:
			t.Error("layout result sender did not exit after cancellation")
			// Unblock the original implementation so a failure is reported cleanly.
			<-done
			<-exited
		}
	})
}
