package shutdownKeeper

import (
	"context"
	"github.com/stretchr/testify/assert"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestShutdownKeeper_SignalShutdown(t *testing.T) {
	var actual int32
	keeper := NewKeeper(KeeperOpts{
		Signals: []os.Signal{syscall.SIGINT},
		OnSignalShutdown: func(_ os.Signal) {
			atomic.StoreInt32(&actual, 1)
		},
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		keeper.signalChan <- syscall.SIGINT
	}()

	keeper.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&actual))
}

func TestShutdownKeeper_ContextDownShutdown(t *testing.T) {
	var actual int32
	ctx, cancel := context.WithCancel(context.Background())
	keeper := NewKeeper(KeeperOpts{
		Context: ctx,
		OnContextDone: func() {
			atomic.StoreInt32(&actual, 1)
		},
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	keeper.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&actual))
}

func TestShutdownKeeper_HoldToken(t *testing.T) {
	keeper := NewKeeper(KeeperOpts{})

	var actual int32
	go func(token HoldToken) {
		defer token.Release()
		atomic.AddInt32(&actual, 1)
	}(keeper.AllocHoldToken())

	go func(token HoldToken) {
		defer token.Release()
		atomic.AddInt32(&actual, 1)
	}(keeper.AllocHoldToken())

	keeper.Wait()
	assert.Equal(t, int32(2), actual)
}

func TestShutdownKeeper_OnShuttingDown(t *testing.T) {
	var actual int32
	ctx, cancel := context.WithCancel(context.Background())
	keeper := NewKeeper(KeeperOpts{
		Context: ctx,
	})
	keeper.OnShuttingDown(func() {
		atomic.StoreInt32(&actual, 1)
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	keeper.Wait()
	assert.Equal(t, int32(1), atomic.LoadInt32(&actual))
}

func TestShutdownKeeper_WaitMultipleTimes(t *testing.T) {
	keeper := NewKeeper(KeeperOpts{
		MaxHoldTime: 2 * time.Second,
		ForceHold:   true,
	})
	token := keeper.AllocHoldToken()
	go token.Release()
	keeper.Wait()

	keeper.maxHoldTime = 10 * time.Second
	startTime := time.Now()
	keeper.Wait()
	assert.Equal(t, 0, int(time.Now().Sub(startTime).Seconds()))
}

func TestShutdownKeeper_ForceHold(t *testing.T) {
	keeper := NewKeeper(KeeperOpts{
		ForceHold:   true,
		MaxHoldTime: 2 * time.Second,
	})

	token := keeper.AllocHoldToken()
	go token.Release()

	startTime := time.Now()
	keeper.Wait()
	assert.Equal(t, 2, int(time.Now().Sub(startTime).Seconds()))
}
