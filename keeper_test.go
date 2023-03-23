package shutdownKeeper

import (
	"context"
	"github.com/stretchr/testify/assert"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestShutdownKeeper_SignalShutdown(t *testing.T) {
	actual := 0
	keeper := NewShutdownKeeper(KeeperOpts{
		Signals: []os.Signal{syscall.SIGINT},
		OnSignalShutdown: func(_ os.Signal) {
			actual = 1
		},
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		keeper.signalChan <- syscall.SIGINT
	}()

	keeper.Wait()
	assert.Equal(t, 1, actual)
}

func TestShutdownKeeper_ContextDownShutdown(t *testing.T) {
	actual := 0
	ctx, cancel := context.WithCancel(context.Background())
	keeper := NewShutdownKeeper(KeeperOpts{
		Context: ctx,
		OnContextDone: func() {
			actual = 1
		},
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	keeper.Wait()
	assert.Equal(t, 1, actual)
}

func TestShutdownKeeper_HoldToken(t *testing.T) {
	keeper := NewShutdownKeeper(KeeperOpts{
		MaxHoldTime: 5 * time.Second,
	})

	startTime := time.Now()

	go func(token HoldToken) {
		time.Sleep(1 * time.Second)
		token.Release()
	}(keeper.AllocHoldToken())

	go func(token HoldToken) {
		time.Sleep(1 * time.Second)
		token.Release()
	}(keeper.AllocHoldToken())

	keeper.Wait()
	assert.Equal(t, 1, int(time.Now().Sub(startTime).Seconds()))

}

func TestShutdownKeeper_WaitMultipleTimes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	keeper := NewShutdownKeeper(KeeperOpts{
		Signals:    []os.Signal{syscall.SIGINT},
		Context:    ctx,
		AlwaysHold: true,
	})
	go func() {
		keeper.signalChan <- syscall.SIGINT
		cancel()
	}()

	keeper.Wait()

	startTime := time.Now()
	keeper.maxHoldTime = 60 * time.Second
	keeper.Wait()
	assert.Equal(t, 0, int(time.Now().Sub(startTime).Seconds()))
}

func TestShutdownKeeper_AlwaysHold(t *testing.T) {
	keeper := NewShutdownKeeper(KeeperOpts{
		AlwaysHold:  true,
		MaxHoldTime: 2 * time.Second,
	})

	startTime := time.Now()
	keeper.Wait()
	assert.Equal(t, 2, int(time.Now().Sub(startTime).Seconds()))
}
