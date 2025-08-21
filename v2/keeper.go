package v2

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

// HoldToken is used by subroutines to listen for shutdown events. It allows subroutines to complete their work.
// Each subroutine that holding a HoldToken should call the Release() method after it finishes its work.
// Once all HoldTokens are released, the shutdown keeper will return from its Wait() method call.
type HoldToken interface {
	// ListenShutdown will block the current goroutine until the shutdown stage is triggered.
	ListenShutdown()

	Release()

	Context() context.Context
}

type ShutdownFunc func()

// KeeperOpts contains options for creating a ShutdownKeeper.
type KeeperOpts struct {
	// Signals specifies the signals that ShutdownKeeper will listen for (for example, syscall.SIGINT, syscall.SIGTERM).
	// Receiving any signal from this list will trigger the shutdown process.
	Signals []os.Signal

	// OnSignal is called when ShutdownKeeper receives any signal provided by Signals.
	// If this option is provided, ShutdownKeeper will not automatically trigger the shutdown process; you need to call the ShutdownFunc function in OnSignal to initiate the shutdown process.
	OnSignal func(os.Signal, ShutdownFunc)

	// ShutdownWhenNoHoldTokens when true, ShutdownKeeper will initiate the shutdown process when there are no HoldTokens allocated or when all HoldTokens are released, no matter if the shutdown process is triggered by signals or by calling StartShutdown method.
	// the default value is false.
	ShutdownWhenNoHoldTokens bool

	// MaxHoldTime is the maximum time that ShutdownKeeper will wait for all HoldTokens to be released when shutdown process is triggered.
	// If the time is exceeded, ShutdownKeeper.Wait() will force return.
	// The default value of MaxHoldTime is 30 seconds.
	MaxHoldTime time.Duration

	// If AlwaysHoldMaxTime is true, ShutdownKeeper will always hold the shutdown process for MaxHoldTime, even if there are no HoldTokens allocated or all HoldTokens are released.
	AlwaysHoldMaxTime bool
}

// ShutdownKeeper manages the graceful shutdown process of a program.
type ShutdownKeeper struct {
	status               int32
	holdingCtx           context.Context
	shuttingFunc         func()
	shutdownWhenNoTokens bool

	signals               []os.Signal
	signalChan            chan os.Signal
	signalReleaseNotifier chan struct{}
	onSignalFunc          func(os.Signal, ShutdownFunc)

	holdTokenNum            int32
	holdTokenFinishNotifier chan struct{}
	holdTokensFinishFunc    func()
	maxHoldTime             time.Duration
	alwaysHoldMaxTime       bool

	shutdownCallbackFuncs []func()
}

func NewKeeper(opts KeeperOpts) *ShutdownKeeper {
	maxHoldTime := opts.MaxHoldTime
	if maxHoldTime <= 0 {
		maxHoldTime = 30 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())
	keeper := &ShutdownKeeper{
		status:               statusReady,
		holdingCtx:           ctx,
		shuttingFunc:         cancel,
		shutdownWhenNoTokens: opts.ShutdownWhenNoHoldTokens,

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

	if k.getHoldingTokenNum() == 0 && k.shutdownWhenNoTokens {
		k.StartShutdown()
	}

	go k.listenSignals()
	defer close(k.signalReleaseNotifier)
	<-k.holdingCtx.Done()

	if k.alwaysHoldMaxTime {
		<-time.After(k.maxHoldTime)
	} else if k.getHoldingTokenNum() > 0 {
		select {
		case <-time.After(k.maxHoldTime):
		case <-k.holdTokenFinishNotifier:
		}
	}

	atomic.StoreInt32(&k.status, statusShutdown)
}

// AllocHoldToken allocates a HoldToken.
func (k *ShutdownKeeper) AllocHoldToken() HoldToken {
	atomic.AddInt32(&k.holdTokenNum, 1)
	return newHoldTokenImpl(k.holdingCtx, sync.OnceFunc(func() {
		if atomic.AddInt32(&k.holdTokenNum, -1) == 0 {
			s := atomic.LoadInt32(&k.status)
			if s == statusWaiting || s == statusShutting {
				k.holdTokensFinishFunc()
				if k.shutdownWhenNoTokens {
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
		go func() {
			for _, fn := range k.shutdownCallbackFuncs {
				fn()
			}
		}()
	}
}

// OnShuttingDown registers a function to be called when the shutdown process is triggered.
func (k *ShutdownKeeper) OnShuttingDown(f func()) {
	s := atomic.LoadInt32(&k.status)
	if s != statusReady && s != statusWaiting {
		return
	}

	k.shutdownCallbackFuncs = append(k.shutdownCallbackFuncs, f)
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
	ctx           context.Context
	releasingFunc func()
}

func newHoldTokenImpl(ctx context.Context, releasingFunc func()) *holdTokenImpl {
	return &holdTokenImpl{
		ctx:           ctx,
		releasingFunc: releasingFunc,
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
