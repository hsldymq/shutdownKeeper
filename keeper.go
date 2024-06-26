package shutdownKeeper

import (
    "context"
    "os"
    "os/signal"
    "sync"
    "sync/atomic"
    "time"
)

// HoldToken is used by subroutines to listen for shutdown events. It allows subroutines to complete their work.
// Each subroutine that holding a HoldToken should call the Release() method after it finishes its work.
// Once all HoldTokens are released, the shutdown keeper will return from its Wait() method call.
type HoldToken interface {
    // ListenShutdown will block the current goroutine until the shutdown stage is triggered.
    ListenShutdown()

    Release()

    Context() context.Context
}

type TokenAllocator interface {
    AllocHoldToken() HoldToken
    OnShuttingDown(func())
}

const (
    _ int32 = iota
    statusReady
    statusWaiting
    statusShutting
    statusShutdown
)

// KeeperOpts contains options for creating a ShutdownKeeper.
type KeeperOpts struct {
    // Signals specifies the signals that ShutdownKeeper will listen for (for example, syscall.SIGINT, syscall.SIGTERM).
    // Receiving any signal from this list will trigger the shutdown process.
    Signals []os.Signal

    // OnSignalShutdown is called when ShutdownKeeper receives any signal provided by Signals.
    OnSignalShutdown func(os.Signal)

    // Context is used to listen for the context.Done() event, which will trigger the shutdown process.
    Context context.Context

    // OnContextDone is called when ShutdownKeeper receives a context.Done() event.
    OnContextDone func()

    // MaxHoldTime is the maximum time that ShutdownKeeper will wait for all HoldTokens to be released when shutdown process is triggered.
    // If the time is exceeded, ShutdownKeeper.Wait() will force return.
    // The default value of MaxHoldTime is 30 seconds.
    MaxHoldTime time.Duration

    // If ForceHold is true, ShutdownKeeper will always hold the shutdown process for MaxHoldTime, even if no HoldToken is allocated or all the HoldTokens are released.
    ForceHold bool
}

// ShutdownKeeper manages the graceful shutdown process of a program.
type ShutdownKeeper struct {
    signals []os.Signal

    signalHandler func(os.Signal)
    signalChan    chan os.Signal

    ctx            context.Context
    ctxDoneHandler func()

    maxHoldTime           time.Duration
    forceHold             bool
    holdingTokenNum       int32
    shutdownHoldChan      chan struct{}
    closeShutdownHoldChan func()

    shuttingChan chan struct{}

    status       int32
    shutdownChan chan struct{}

    tokenCtx       context.Context
    tokenCtxCancel func()
}

func NewKeeper(opts KeeperOpts) *ShutdownKeeper {
    maxHoldTime := opts.MaxHoldTime
    if maxHoldTime == 0 {
        maxHoldTime = 30 * time.Second
    }

    ctx, cancel := context.WithCancel(context.Background())
    keeper := &ShutdownKeeper{
        signals:       opts.Signals,
        signalHandler: opts.OnSignalShutdown,
        signalChan:    make(chan os.Signal, 1),

        ctx:            opts.Context,
        ctxDoneHandler: opts.OnContextDone,

        maxHoldTime:      maxHoldTime,
        forceHold:        opts.ForceHold,
        holdingTokenNum:  0,
        shutdownHoldChan: make(chan struct{}),

        shuttingChan: make(chan struct{}),

        status:       statusReady,
        shutdownChan: make(chan struct{}),

        tokenCtx:       ctx,
        tokenCtxCancel: cancel,
    }
    keeper.closeShutdownHoldChan = sync.OnceFunc(func() {
        close(keeper.shutdownHoldChan)
    })

    return keeper
}

// Wait blocks the current goroutine until the shutdown process is finished.
// It listens to Signals and Context if provided.
// Once any of them is triggered, the graceful shutdown process will be performed.
// If the ShutdownKeeper is already in shutdown status, Wait will return immediately.
func (k *ShutdownKeeper) Wait() {
    if !atomic.CompareAndSwapInt32(&k.status, statusReady, statusWaiting) {
        return
    }

    if len(k.signals) == 0 && k.ctx == nil && k.getHoldingTokenNum() == 0 {
        k.startShutdown(nil)
    } else {
        go k.listenSignals()
        go k.listenContext()
    }
    <-k.shuttingChan
    k.tokenCtxCancel()

    if k.forceHold {
        <-time.After(k.maxHoldTime)
    } else if k.getHoldingTokenNum() > 0 {
        select {
        case <-time.After(k.maxHoldTime):
        case <-k.shutdownHoldChan:
        }
    }

    k.closeShutdownHoldChan()
    atomic.StoreInt32(&k.status, statusShutdown)
    close(k.shutdownChan)
}

// AllocHoldToken allocates a hold token.
func (k *ShutdownKeeper) AllocHoldToken() HoldToken {
    atomic.AddInt32(&k.holdingTokenNum, 1)
    return newHoldTokenImpl(func() {
        if atomic.AddInt32(&k.holdingTokenNum, -1) == 0 {
            s := atomic.LoadInt32(&k.status)
            if s == statusWaiting || s == statusShutting {
                k.closeShutdownHoldChan()
                k.startShutdown(nil)
            }
        }
    }, k.tokenCtx)
}

// OnShuttingDown registers a function to be called when the shutdown process is triggered.
func (k *ShutdownKeeper) OnShuttingDown(f func()) {
    s := atomic.LoadInt32(&k.status)
    if s != statusReady && s != statusWaiting {
        return
    }

    go func(token HoldToken) {
        defer token.Release()
        token.ListenShutdown()
        f()
    }(k.AllocHoldToken())
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
            k.startShutdown(nil)
            if k.signalHandler != nil {
                k.signalHandler(s)
            }
        case <-k.shutdownChan:
            break loop
        }
    }

    signal.Stop(k.signalChan)
    close(k.signalChan)
}

func (k *ShutdownKeeper) listenContext() {
    if k.ctx == nil {
        return
    }

    select {
    case <-k.ctx.Done():
        k.startShutdown(k.ctxDoneHandler)
    case <-k.shutdownChan:
    }
}

func (k *ShutdownKeeper) startShutdown(eventFunc func()) bool {
    if atomic.CompareAndSwapInt32(&k.status, statusWaiting, statusShutting) || atomic.CompareAndSwapInt32(&k.status, statusReady, statusShutting) {
        defer close(k.shuttingChan)
        if eventFunc != nil {
            eventFunc()
        }
        return true
    }
    return false
}

// getHoldingTokenNum returns the number of hold tokens that have not been released yet.
func (k *ShutdownKeeper) getHoldingTokenNum() int32 {
    return atomic.LoadInt32(&k.holdingTokenNum)
}

type holdTokenImpl struct {
    releasingFunc func()
    ctx           context.Context
}

func newHoldTokenImpl(releasingFunc func(), ctx context.Context) *holdTokenImpl {
    return &holdTokenImpl{
        releasingFunc: sync.OnceFunc(releasingFunc),
        ctx:           ctx,
    }
}

func (kt *holdTokenImpl) ListenShutdown() {
    <-kt.Context().Done()
}

func (kt *holdTokenImpl) Release() {
    kt.releasingFunc()
}

func (kt *holdTokenImpl) Context() context.Context {
    return kt.ctx
}
