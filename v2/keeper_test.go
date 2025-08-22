package v2

import (
    "context"
    "os"
    "sync/atomic"
    "syscall"
    "testing"
    "time"
)

func TestShutdownKeeper_SignalShutdown(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals: []os.Signal{syscall.SIGINT},
    })
    start := time.Now()
    go func() {
        time.Sleep(500 * time.Millisecond)
        keeper.signalChan <- syscall.SIGINT
    }()
    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed < 0.5 || elapsed >= 0.6 {
        t.Fatalf("Expected shutdown time to be at least 0.5 seconds and less than 0.6 seconds, got: %f seconds", elapsed)
    }
}

func TestShutdownKeeper_SignalCustomShutdown(t *testing.T) {
    signalCount := 0
    keeper := NewKeeper(KeeperOpts{
        Signals: []os.Signal{syscall.SIGINT},
        OnSignal: func(signal os.Signal, shutdown ShutdownFunc) {
            signalCount++
            if signalCount >= 2 {
                shutdown()
            }
        },
    })
    start := time.Now()
    go func() {
        time.Sleep(500 * time.Millisecond)
        keeper.signalChan <- syscall.SIGINT
    }()
    go func() {
        time.Sleep(time.Second)
        keeper.signalChan <- syscall.SIGINT
    }()
    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed < 1 || elapsed >= 1.1 {
        t.Fatalf("Expected shutdown time to be at least 1 second and less than 1.1 seconds, got: %f seconds", elapsed)
    }
    if signalCount != 2 {
        t.Fatalf("Expected signal count to be 2, got: %d", signalCount)
    }
}

func TestShutdownKeeper_HoldTokens(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals: []os.Signal{syscall.SIGINT},
    })

    start := time.Now()
    var actual int32
    keeper.AllocHoldToken().GoListenThenDo(func(_ context.Context) {
        time.Sleep(time.Second)
        atomic.AddInt32(&actual, 1)
    })

    keeper.AllocHoldToken().GoListenThenDo(func(_ context.Context) {
        time.Sleep(500 * time.Millisecond)
        atomic.AddInt32(&actual, 1)
    })

    go func() {
        time.Sleep(500 * time.Millisecond)
        keeper.signalChan <- syscall.SIGINT
    }()

    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed < 1.5 || elapsed >= 1.6 {
        t.Fatalf("Expected shutdown time to be at least 1.5 seconds and less than 1.6 seconds, got: %f seconds", elapsed)
    }
    actualVal := atomic.LoadInt32(&actual)
    if actualVal != 2 {
        t.Fatalf("expect: 2, actual: %d", actualVal)
    }
}

func TestShutdownKeeper_ShutdownWhenAllHoldTokensReleased(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        TokenReleaseMode: ShutdownWhenNoTokens,
    })

    start := time.Now()
    var actual int32

    keeper.AllocHoldToken().GoRun(func() {
        time.Sleep(time.Second)
        atomic.AddInt32(&actual, 1)
    })

    keeper.AllocHoldToken().GoRun(func() {
        time.Sleep(500 * time.Millisecond)
        atomic.AddInt32(&actual, 1)
    })

    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed < 1 || elapsed >= 1.1 {
        t.Fatalf("Expected shutdown time to be at least 1 second and less than 1.1 seconds, got: %f seconds", elapsed)
    }
    actualVal := atomic.LoadInt32(&actual)
    if actualVal != 2 {
        t.Fatalf("expect: 2, actual: %d", actualVal)
    }
}

func TestShutdownKeeper_MaxHoldTime(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals:     []os.Signal{syscall.SIGINT},
        MaxHoldTime: 1 * time.Second,
    })

    start := time.Now()
    keeper.AllocHoldToken().GoListenThenDo(func(_ context.Context) {
        time.Sleep(5 * time.Second)
    })

    keeper.signalChan <- syscall.SIGINT

    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed < 1 || elapsed >= 1.1 {
        t.Fatalf("Expected shutdown time to be at least 1 second and less than 1.1 seconds, got: %f seconds", elapsed)
    }
}

func TestShutdownKeeper_AlwaysHoldMaxTime(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals:           []os.Signal{syscall.SIGINT},
        MaxHoldTime:       2 * time.Second,
        AlwaysHoldMaxTime: true,
    })

    start := time.Now()
    keeper.AllocHoldToken().GoListenThenDo(func(_ context.Context) {
        time.Sleep(500 * time.Millisecond)
    })
    keeper.signalChan <- syscall.SIGINT

    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed < 2 || elapsed >= 2.1 {
        t.Fatalf("Expected shutdown time to be at least 2 seconds and less than 2.1 seconds, got: %f seconds", elapsed)
    }
}

func TestShutdownKeeper_CleanupTaskReachHoldingDeadline(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals:     []os.Signal{syscall.SIGINT},
        MaxHoldTime: 2 * time.Second,
    })

    elapsed := float64(0)
    keeper.AllocHoldToken().GoListenThenDo(func(ctx context.Context) {
        start := time.Now()
        <-ctx.Done()
        elapsed = time.Since(start).Seconds()
    })

    keeper.signalChan <- syscall.SIGINT
    keeper.Wait()

    if elapsed < 2 || elapsed >= 2.1 {
        t.Fatalf("Expected the cleanup task to reach the max hold time, which should be at least 2 seconds and less than 2.1 seconds, but got: %f seconds.", elapsed)
    }
}

func TestShutdownKeeper_OnShuttingDown(t *testing.T) {
    // case 1
    var actual int32
    keeper := NewKeeper(KeeperOpts{})
    keeper.OnShuttingDown(func() {
        atomic.StoreInt32(&actual, 1)
    })
    go func() {
        time.Sleep(50 * time.Millisecond)
        keeper.StartShutdown()
    }()

    keeper.Wait()

    actualVal := atomic.LoadInt32(&actual)
    if actualVal != 1 {
        t.Fatalf("expect: 1, actual: %d", actualVal)
    }

    // case 2: call OnShuttingDown after shutdown
    keeper = NewKeeper(KeeperOpts{
        TokenReleaseMode: ShutdownWhenNoTokens,
    })
    keeper.AllocHoldToken().Release()
    keeper.Wait()
    actual = 0
    keeper.OnShuttingDown(func() {
        actual = 1
    })
    keeper.Wait()
    if actual != 0 {
        t.Fatalf("the OnShuttingDown callback should not be called after shutdown, expect: 0, actual: %d", actual)
    }
}

func TestShutdownKeeper_WaitMultipleTimes(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        TokenReleaseMode: ShutdownWhenNoTokens,

        MaxHoldTime:       1 * time.Second,
        AlwaysHoldMaxTime: true,
    })

    keeper.AllocHoldToken().GoRun(func() {
        time.Sleep(500 * time.Millisecond)
    })

    keeper.Wait()

    keeper.maxHoldTime = 10 * time.Second
    start := time.Now()
    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    if elapsed > 0.01 {
        t.Fatalf("Expected second Wait to return immediately, but it took: %f seconds", elapsed)
    }
}
