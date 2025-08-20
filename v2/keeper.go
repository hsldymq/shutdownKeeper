package v2

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

type ShutdownFunc func()

const (
	statusReady = iota
	statusWaiting
	statusShutting
	statusShutdown
)

// KeeperOpts contains options for creating a ShutdownKeeper.
type KeeperOpts struct {
	// Signals specifies the signals that ShutdownKeeper will listen for (for example, syscall.SIGINT, syscall.SIGTERM).
	// Receiving any signal from this list will trigger the shutdown process.
	Signals []os.Signal

	// OnSignal is called when ShutdownKeeper receives any signal provided by Signals.
	OnSignal func(os.Signal, ShutdownFunc)

	// MaxHoldTime is the maximum time that ShutdownKeeper will wait for all HoldTokens to be released when shutdown process is triggered.
	// If the time is exceeded, ShutdownKeeper.Wait() will force return.
	// The default value of MaxHoldTime is 30 seconds.
	MaxHoldTime time.Duration

	// If AlwaysHoldMaxTime is true, ShutdownKeeper will always hold the shutdown process for MaxHoldTime, even if no HoldToken is allocated or all the HoldTokens are released.
	AlwaysHoldMaxTime bool
}

// ShutdownKeeper manages the graceful shutdown process of a program.
type ShutdownKeeper struct {
	status             int32
	shuttingNotifyChan chan struct{}
	shutdownChan       chan struct{}

	signals      []os.Signal
	onSignalFunc func(os.Signal, ShutdownFunc)
	signalChan   chan os.Signal

	holdTokenNum          int32
	holdTokenCtx          context.Context
	holdTokenShuttingFunc func()
	shutdownHoldChan      chan struct{}
	closeShutdownHoldChan func()
	maxHoldTime           time.Duration
	alwaysHoldMaxTime     bool
}

func NewKeeper(opts KeeperOpts) *ShutdownKeeper {
	maxHoldTime := opts.MaxHoldTime
	if maxHoldTime <= 0 {
		maxHoldTime = 30 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	keeper := &ShutdownKeeper{
		status:             statusReady,
		shuttingNotifyChan: make(chan struct{}),
		shutdownChan:       make(chan struct{}),

		signals:      opts.Signals,
		onSignalFunc: opts.OnSignal,
		signalChan:   make(chan os.Signal, 1),

		holdTokenNum:          0,
		holdTokenCtx:          ctx,
		holdTokenShuttingFunc: cancel,
		shutdownHoldChan:      make(chan struct{}),
		maxHoldTime:           maxHoldTime,
		alwaysHoldMaxTime:     opts.AlwaysHoldMaxTime,
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

	if k.getHoldingTokenNum() == 0 {
		k.startShutdown()
	}
	go k.listenSignals()
	<-k.shuttingNotifyChan
	k.holdTokenShuttingFunc()

	if k.alwaysHoldMaxTime {
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
	atomic.AddInt32(&k.holdTokenNum, 1)
	return newHoldTokenImpl(k.holdTokenCtx, func() {
		if atomic.AddInt32(&k.holdTokenNum, -1) == 0 {
			s := atomic.LoadInt32(&k.status)
			if s == statusWaiting || s == statusShutting {
				k.closeShutdownHoldChan()
				k.startShutdown()
			}
		}
	})
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
			if k.onSignalFunc == nil {
				k.startShutdown()
			} else {
				k.onSignalFunc(s, k.startShutdown)
			}
		case <-k.shutdownChan:
			break loop
		}
	}

	signal.Stop(k.signalChan)
	close(k.signalChan)
}

func (k *ShutdownKeeper) startShutdown() {
	if atomic.CompareAndSwapInt32(&k.status, statusWaiting, statusShutting) || atomic.CompareAndSwapInt32(&k.status, statusReady, statusShutting) {
		defer close(k.shuttingNotifyChan)
	}
}

// getHoldingTokenNum returns the number of hold tokens that have not been released yet.
func (k *ShutdownKeeper) getHoldingTokenNum() int32 {
	return atomic.LoadInt32(&k.holdTokenNum)
}

type holdTokenImpl struct {
	ctx           context.Context
	releasingFunc func()
}

func newHoldTokenImpl(ctx context.Context, releasingFunc func()) *holdTokenImpl {
	return &holdTokenImpl{
		ctx:           ctx,
		releasingFunc: sync.OnceFunc(releasingFunc),
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
