package agent

import (
	"context"
	"errors"
	"time"
)

func (a *Agent) runSpoke(parent context.Context) (result error) {
	runtime, err := newPeerRuntime(a)
	if err != nil {
		return err
	}
	return a.runSpokeRuntime(parent, runtime)
}

func (a *Agent) runSpokeRuntime(parent context.Context, runtime *peerRuntime) (result error) {
	a.status.setState("running")
	a.publishPeerRuntime(runtime)
	defer a.clearPeerRuntime(runtime)
	ctx, cancel := context.WithCancel(parent)
	type deviceResult struct {
		err         error
		spontaneous bool
	}
	deviceDone := make(chan deviceResult, 1)
	go func() { err := runtime.readDevice(ctx); deviceDone <- deviceResult{err, ctx.Err() == nil}; cancel() }()
	defer func() {
		cancel()
		_ = runtime.Close()
		// A real TUN read can ignore context once inside the driver. Interrupt
		// it after the independent transports stop, before joining the reader.
		_ = a.closeDevice()
		device := <-deviceDone
		var permanent *controlCompatibilityError
		if device.spontaneous && device.err != nil && !errors.Is(device.err, context.Canceled) && !errors.As(result, &permanent) {
			result = device.err
		}
	}()
	delay := spokeReconnectMinDelay
	for {
		started := time.Now()
		err := runtime.control.connect(ctx)
		runtime.control.disconnect()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var permanent *controlCompatibilityError
		if errors.As(err, &permanent) {
			return err
		}
		if time.Since(started) >= spokeStableResetAfter {
			delay = spokeReconnectMinDelay
		}
		wait := jitterDelay(delay)
		a.log.Warn("coordinator control ended, reconnecting", "err", err, "delay", wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = nextReconnectDelay(delay)
	}
}
func nextReconnectDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return spokeReconnectMinDelay
	}
	next := delay * 2
	if next > spokeReconnectMaxDelay {
		return spokeReconnectMaxDelay
	}
	return next
}
func jitterDelay(delay time.Duration) time.Duration {
	if delay <= 0 {
		return spokeReconnectMinDelay
	}
	window := delay / 5
	if window <= 0 {
		return delay
	}
	offset := time.Duration(time.Now().UnixNano()%int64(window*2+1)) - window
	result := delay + offset
	if result < spokeReconnectMinDelay {
		return spokeReconnectMinDelay
	}
	if result > spokeReconnectMaxDelay {
		return spokeReconnectMaxDelay
	}
	return result
}
