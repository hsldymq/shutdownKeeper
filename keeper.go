package shutdownKeeper

import (
    "context"
    "os"
    "os/signal"
    "sync"
    "sync/atomic"
    "time"
)

const (
    statusReady = iota
    statusWaiting
    statusShutting
    statusShutdown
)

// HoldToken is used by subroutines to listen for the shutdown event. It allows subroutines to complete their work.
// Each subroutine that holding a HoldToken should call the Release() method after it finishes its work.
// Once all HoldTokens are released, the shutdown keeper will return from its Wait() method call.
type HoldToken interface {
    // ListenShutdown will block the current goroutine until the shutdown event is triggered.
    ListenShutdown()

    // ListeningContext is the context that will be canceled when the shutdown event is triggered, for example, when a signal is received or when the StartShutdown method is called.
    // this context is the one that ListenShutdown method blocks on.
    ListeningContext() context.Context

    // Release should always be called after the subroutine finishes its work.
    Release()

    // HoldingDeadlineContext is the context that will be canceled when the MaxHoldTime is exceeded during the shutdown process.
    HoldingDeadlineContext() context.Context

    // GoListenThenDo is a shortcut that starts a goroutine to listen for the shutdown event, when the shutdown event is triggered, it runs the provided function. After the function execution is completed, the HoldToken will be released.
    // the context that is passed to the function is the one returned by HoldingDeadlineContext method.
    GoListenThenDo(func(ctx context.Context))

    // GoRun is a shortcut that starts a goroutine and run the provided function immediately. After the function execution is completed, the HoldToken will be released.
    GoRun(func())
}

type TokenReleaseMode int

const (
    // WaitForTriggering when this mode is set, the shutdown process will be initiated only when signals are received or when StartShutdown method is called, even all HoldTokens are released.
    WaitForTriggering TokenReleaseMode = iota

    // ShutdownWhenNoTokens when this mode is set, the shutdown process will be initiated when there are no HoldTokens allocated or when all HoldTokens are released, no matter if the shutdown process is triggered by signals or by calling StartShutdown method.
    ShutdownWhenNoTokens
)

type ShutdownFunc func()

// KeeperOpts contains options for creating a ShutdownKeeper.
type KeeperOpts struct {
    // Signals specifies the signals that ShutdownKeeper will listen for (for example, syscall.SIGINT, syscall.SIGTERM).
    // Receiving any signal from this list will trigger the shutdown process.
    Signals []os.Signal

    // OnSignal is called when ShutdownKeeper receives any signal provided by Signals.
    // If this option is provided, ShutdownKeeper will not automatically trigger the shutdown process; you need to call the ShutdownFunc function in OnSignal to initiate the shutdown process.
    OnSignal func(os.Signal, ShutdownFunc)

    // TokenReleaseMode represents the behavior of ShutdownKeeper when all HoldTokens are released or when no HoldTokens are allocated.
    // The default value is WaitForTriggering.
    TokenReleaseMode TokenReleaseMode

    // MaxHoldTime is the maximum time that ShutdownKeeper will wait for all HoldTokens to be released when shutdown process is triggered.
    // If the time is exceeded, ShutdownKeeper.Wait() will force return.
    // The default value of MaxHoldTime is 30 seconds.
    MaxHoldTime time.Duration

    // If AlwaysHoldMaxTime is true, ShutdownKeeper will always hold the shutdown process for MaxHoldTime, even if there are no HoldTokens allocated or all HoldTokens are released.
    AlwaysHoldMaxTime bool
}

// ShutdownKeeper manages the graceful shutdown process of a program.
type ShutdownKeeper struct {
    status                int32
    listeningCtx          context.Context
    shuttingFunc          func()
    tokenReleaseMode      TokenReleaseMode
    holdingDeadlineCtx    context.Context
    holdingDeadlineCancel func()

    signals               []os.Signal
    signalChan            chan os.Signal
    signalReleaseNotifier chan struct{}
    onSignalFunc          func(os.Signal, ShutdownFunc)

    holdTokenNum            int32
    holdTokenFinishNotifier chan struct{}
    holdTokensFinishFunc    func()
    maxHoldTime             time.Duration
    alwaysHoldMaxTime       bool
}

func NewKeeper(opts KeeperOpts) *ShutdownKeeper {
    maxHoldTime := opts.MaxHoldTime
    if maxHoldTime <= 0 {
        maxHoldTime = 30 * time.Second
    }

    tokenReleaseMode := opts.TokenReleaseMode
    if tokenReleaseMode != WaitForTriggering && tokenReleaseMode != ShutdownWhenNoTokens {
        tokenReleaseMode = WaitForTriggering
    }

    listeningCtx, listeningCancel := context.WithCancel(context.Background())
    holdingDeadlineCtx, deadlineCancel := context.WithCancel(context.Background())
    keeper := &ShutdownKeeper{
        status:                statusReady,
        listeningCtx:          listeningCtx,
        shuttingFunc:          listeningCancel,
        tokenReleaseMode:      tokenReleaseMode,
        holdingDeadlineCtx:    holdingDeadlineCtx,
        holdingDeadlineCancel: sync.OnceFunc(deadlineCancel),

        signals:               opts.Signals,
        signalChan:            make(chan os.Signal, 1),
        signalReleaseNotifier: make(chan struct{}),
        onSignalFunc:          opts.OnSignal,

        holdTokenNum:            0,
        holdTokenFinishNotifier: make(chan struct{}),
        maxHoldTime:             maxHoldTime,
        alwaysHoldMaxTime:       opts.AlwaysHoldMaxTime,
    }
    keeper.holdTokensFinishFunc = sync.OnceFunc(func() {
        close(keeper.holdTokenFinishNotifier)
    })

    return keeper
}

// Wait blocks the current goroutine until the shutdown process is finished.
func (k *ShutdownKeeper) Wait() {
    if !atomic.CompareAndSwapInt32(&k.status, statusReady, statusWaiting) {
        return
    }

    if k.getHoldingTokenNum() == 0 && k.tokenReleaseMode == ShutdownWhenNoTokens {
        k.StartShutdown()
    }

    go k.listenSignals()
    defer close(k.signalReleaseNotifier)
    <-k.listeningCtx.Done()

    reachMaxHoldTime := false
    if k.alwaysHoldMaxTime {
        <-time.After(k.maxHoldTime)
        reachMaxHoldTime = true
        k.holdingDeadlineCancel()
    } else if k.getHoldingTokenNum() > 0 {
        select {
        case <-time.After(k.maxHoldTime):
            reachMaxHoldTime = true
            k.holdingDeadlineCancel()
        case <-k.holdTokenFinishNotifier:
            defer k.holdingDeadlineCancel()
        }
    }

    if reachMaxHoldTime {
        // add a small delay for the cleanup
        time.Sleep(50 * time.Millisecond)
    }
    atomic.StoreInt32(&k.status, statusShutdown)
}

// AllocHoldToken allocates a HoldToken.
func (k *ShutdownKeeper) AllocHoldToken() HoldToken {
    atomic.AddInt32(&k.holdTokenNum, 1)
    return newHoldTokenImpl(k.listeningCtx, k.holdingDeadlineCtx, sync.OnceFunc(func() {
        if atomic.AddInt32(&k.holdTokenNum, -1) == 0 {
            s := atomic.LoadInt32(&k.status)
            if s == statusWaiting || s == statusShutting {
                k.holdTokensFinishFunc()
                if k.tokenReleaseMode == ShutdownWhenNoTokens {
                    k.StartShutdown()
                }
            }
        }
    }))
}

// StartShutdown initiates the shutdown process.
func (k *ShutdownKeeper) StartShutdown() {
    if atomic.CompareAndSwapInt32(&k.status, statusWaiting, statusShutting) || atomic.CompareAndSwapInt32(&k.status, statusReady, statusShutting) {
        k.shuttingFunc()
    }
}

func (k *ShutdownKeeper) listenSignals() {
    if len(k.signals) == 0 {
        return
    }

    signal.Notify(k.signalChan, k.signals...)
loop:
    for {
        select {
        case s := <-k.signalChan:
            if k.onSignalFunc == nil {
                k.StartShutdown()
            } else {
                k.onSignalFunc(s, k.StartShutdown)
            }
        case <-k.signalReleaseNotifier:
            break loop
        }
    }

    signal.Stop(k.signalChan)
    close(k.signalChan)
}

func (k *ShutdownKeeper) getHoldingTokenNum() int32 {
    return atomic.LoadInt32(&k.holdTokenNum)
}

type holdTokenImpl struct {
    listeningCtx       context.Context
    holdingDeadlineCtx context.Context
    releasingFunc      func()
}

func newHoldTokenImpl(listeningCtx context.Context, holdingDeadlineCtx context.Context, releasingFunc func()) *holdTokenImpl {
    return &holdTokenImpl{
        listeningCtx:       listeningCtx,
        holdingDeadlineCtx: holdingDeadlineCtx,
        releasingFunc:      releasingFunc,
    }
}

func (kt *holdTokenImpl) ListenShutdown() {
    <-kt.ListeningContext().Done()
}

func (kt *holdTokenImpl) ListeningContext() context.Context {
    return kt.listeningCtx
}

func (kt *holdTokenImpl) Release() {
    kt.releasingFunc()
}

func (kt *holdTokenImpl) HoldingDeadlineContext() context.Context {
    return kt.holdingDeadlineCtx
}

func (kt *holdTokenImpl) GoListenThenDo(f func(ctx context.Context)) {
    go func() {
        defer kt.Release()
        kt.ListenShutdown()
        f(kt.holdingDeadlineCtx)
    }()
}

func (kt *holdTokenImpl) GoRun(f func()) {
    go func() {
        defer kt.Release()
        f()
    }()
}
