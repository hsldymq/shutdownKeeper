package shutdownKeeper

import (
    "context"
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
    if actual != 1 {
        t.Fatalf("expect: 1, actual: %d", actual)
    }
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
    if actual != 1 {
        t.Fatalf("expect: 1, actual: %d", actual)
    }
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
    if actual != 2 {
        t.Fatalf("expect: 2, actual: %d", actual)
    }
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
    if actual != 1 {
        t.Fatalf("expect: 1, actual: %d", actual)
    }
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

    actual := int(time.Now().Sub(startTime).Seconds())
    if actual != 0 {
        t.Fatalf("expect: 0, actual: %d", actual)
    }
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

    actual := int(time.Now().Sub(startTime).Seconds())
    if actual != 2 {
        t.Fatalf("expect: 2, actual: %d", actual)
    }
}
