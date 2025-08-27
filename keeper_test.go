package shutdownKeeper

import (
    "context"
    "os"
    "sync"
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
    keeper.AllocHoldToken().DoOnShutdown(func(_ context.Context) {
        time.Sleep(time.Second)
        atomic.AddInt32(&actual, 1)
    })

    keeper.AllocHoldToken().DoOnShutdown(func(_ context.Context) {
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

    keeper.AllocHoldToken().GoRunWithCtx(func(_ context.Context) {
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

    // case 2:
    keeper = NewKeeper(KeeperOpts{
        TokenReleaseMode: ShutdownWhenNoTokens,
    })
    start = time.Now()
    keeper.Wait()
    elapsed = time.Since(start).Seconds()

    if elapsed >= 0.1 {
        t.Fatalf("Expected shutdown should return immediately when no hold tokens, but took: %f seconds", elapsed)
    }
}

func TestShutdownKeeper_MaxHoldTime(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals:     []os.Signal{syscall.SIGINT},
        MaxHoldTime: 1 * time.Second,
    })

    start := time.Now()
    keeper.AllocHoldToken().DoOnShutdown(func(_ context.Context) {
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
    keeper.AllocHoldToken().DoOnShutdown(func(_ context.Context) {
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

    elapsedChan := make(chan float64)
    token := keeper.AllocHoldToken()
    go func(token HoldToken) {
        defer token.Release()
        token.ListenShutdown()

        start := time.Now()
        <-token.DeadlineContext().Done()
        elapsed := time.Since(start).Seconds()
        elapsedChan <- elapsed
    }(token)

    keeper.signalChan <- syscall.SIGINT
    keeper.Wait()

    elapsed := <-elapsedChan
    if elapsed < 2 || elapsed >= 2.1 {
        t.Fatalf("Expected the cleanup task to reach the max hold time, which should be at least 2 seconds and less than 2.1 seconds, but got: %f seconds.", elapsed)
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

func TestHoldToken_ChainToken(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        Signals: []os.Signal{syscall.SIGINT},
    })

    var executionOrder []int
    l := &sync.Mutex{}
    addToOrder := func(order int) {
        l.Lock()
        defer l.Unlock()
        executionOrder = append(executionOrder, order)
    }

    parentToken := keeper.AllocHoldToken()
    chainedToken := parentToken.AllocChainedToken()

    // Set up parent token to do some work then release
    parentToken.DoOnShutdown(func(ctx context.Context) {
        addToOrder(1)
        time.Sleep(200 * time.Millisecond) // Simulate work
        addToOrder(2)
        // parentToken will be released automatically after this function
    })

    // Set up chained token to wait for parent release
    chainedToken.DoOnShutdown(func(ctx context.Context) {
        addToOrder(3)
        time.Sleep(100 * time.Millisecond) // Simulate work
        addToOrder(4)
    })

    go func() {
        // Start shutdown process after a short delay
        time.Sleep(100 * time.Millisecond)
        keeper.StartShutdown()
    }()

    start := time.Now()
    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    expectedOrder := []int{1, 2, 3, 4}
    if len(executionOrder) != len(expectedOrder) {
        t.Fatalf("Expected execution order length %d, got %d. Order: %v", len(expectedOrder), len(executionOrder), executionOrder)
    }

    for i, expected := range expectedOrder {
        if executionOrder[i] != expected {
            t.Fatalf("Expected execution order %v, got %v", expectedOrder, executionOrder)
        }
    }

    // Total time should be at least 400ms (100ms shutdown delay + 200ms parent + 100ms chained)
    if elapsed < 0.4 || elapsed >= 0.410 {
        t.Fatalf("Expected total execution time to be at least 400ms and less than 410ms, got: %f seconds", elapsed)
    }
}

func TestHoldToken_ChainToken_MultipleChains(t *testing.T) {
    keeper := NewKeeper(KeeperOpts{
        TokenReleaseMode: ShutdownWhenNoTokens,
    })

    var executionOrder []int
    l := &sync.Mutex{}
    addToOrder := func(order int) {
        l.Lock()
        defer l.Unlock()
        executionOrder = append(executionOrder, order)
    }

    // Create a chain: parent -> chainedA1 & chainedA2 -> chainedB
    parent := keeper.AllocHoldToken()
    chainedA1 := parent.AllocChainedToken()
    chainedA2 := parent.AllocChainedToken()
    chainedB := chainedA2.AllocChainedToken()

    parent.DoOnShutdown(func(_ context.Context) {
        addToOrder(1)
        time.Sleep(100 * time.Millisecond)
    })

    chainedA1.DoOnShutdown(func(_ context.Context) {
        time.Sleep(100 * time.Millisecond)
        addToOrder(2)
    })

    chainedA2.DoOnShutdown(func(_ context.Context) {
        time.Sleep(50 * time.Millisecond)
        addToOrder(3)
    })

    chainedB.DoOnShutdown(func(_ context.Context) {
        time.Sleep(100 * time.Millisecond)
        addToOrder(4)
    })

    go func() {
        // Start shutdown process after a short delay
        time.Sleep(100 * time.Millisecond)
        keeper.StartShutdown()
    }()

    start := time.Now()
    keeper.Wait()
    elapsed := time.Since(start).Seconds()

    expectedOrder := []int{1, 3, 2, 4}
    if len(executionOrder) != len(expectedOrder) {
        t.Fatalf("Expected execution order length %d, got %d. Order: %v", len(expectedOrder), len(executionOrder), executionOrder)
    }

    for i, expected := range expectedOrder {
        if executionOrder[i] != expected {
            t.Fatalf("Expected execution order %v, got %v", expectedOrder, executionOrder)
        }
    }

    // Total time should be at least 350ms (100ms shutdown delay + 100ms parent + 100ms chainedA1+chainedA2(concurrently) + 100ms chainedB(start from release of chainedA2))
    if elapsed < 0.35 || elapsed >= 0.36 {
        t.Fatalf("Expected total execution time to be at least 350ms and less than 360ms, got: %f seconds", elapsed)
    }
}
