package shutdownKeeper

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

// HoldToken is allocated to subroutines to listen the shutdown event, and let the subroutines finish their work.
// each subroutine holding a HoldToken should call Release() method when it finishes its work.
// once all the HoldTokens are released, the shutdown keeper will return from its Wait() method call.
type HoldToken interface {
	ListenShutdown() <-chan struct{}
	Release()
}

type holdTokenImpl struct {
	released     bool
	releasedFunc func()
	releaseLock  *sync.Mutex

	shutdownNotifyChan <-chan struct{}
}

func (kt *holdTokenImpl) Release() {
	kt.releaseLock.Lock()
	defer kt.releaseLock.Unlock()
	if !kt.released {
		kt.released = true
		go kt.releasedFunc()
	}
}

func (kt *holdTokenImpl) ListenShutdown() <-chan struct{} {
	return kt.shutdownNotifyChan
}

const (
	keeperInit int32 = iota
	keeperRunning
	keeperShutdown
)

// KeeperOpts is the options for creating ShutdownKeeper.
type KeeperOpts struct {
	// Signals determines the signals that ShutdownKeeper will listen to.
	// any signal in this slice will trigger the shutdown process.
	Signals []os.Signal

	// Context listens to the context.Done() event, the event will trigger the shutdown process.
	Context context.Context

	// OnSignalShutdown that will be called when ShutdownKeeper receives any signal provided by Signals.
	// this allows you to perform graceful shutdown by stopping the process before the program is actually shutdown.
	// If both signal and context.Done() are triggered,
	//	only one of OnSignalShutdown and OnContextDone will be called depending on which event is triggered first.
	OnSignalShutdown func(os.Signal)

	// OnContextDone will be called when ShutdownKeeper receives a context.Done() event.
	// If both signal and context.Done() are triggered,
	//	only one of OnSignalShutdown and OnContextDone will be called depending on which event is triggered first.
	OnContextDone func()

	// MaxHoldTime is the maximum time that ShutdownKeeper will wait for all HoldTokens to be released.
	// if the time is exceeded, ShutdownKeeper.Wait() will force return.
	MaxHoldTime time.Duration

	// if AlwaysHold is true, ShutdownKeeper will always hold the shutdown process for MaxHoldTime even if there is no HoldToken being allocated.
	AlwaysHold bool
}

type ShutdownKeeper struct {
	signals       []os.Signal
	signalHandler func(os.Signal)
	signalChan    chan os.Signal

	ctx        context.Context
	ctxHandler func()

	maxHoldTime      time.Duration
	holdTokenNum     int32
	alwaysHold       bool
	tokenGroup       *sync.WaitGroup
	tokenReleaseChan chan struct{}

	status             int32
	shutdownEventChan  chan struct{}
	shutdownNotifyChan chan struct{}
}

func NewKeeper(opts KeeperOpts) *ShutdownKeeper {
	return &ShutdownKeeper{
		signals:       opts.Signals,
		signalHandler: opts.OnSignalShutdown,
		signalChan:    make(chan os.Signal, 1),

		ctx:        opts.Context,
		ctxHandler: opts.OnContextDone,

		maxHoldTime:      opts.MaxHoldTime,
		holdTokenNum:     0,
		alwaysHold:       opts.AlwaysHold,
		tokenGroup:       &sync.WaitGroup{},
		tokenReleaseChan: make(chan struct{}),

		status:             keeperInit,
		shutdownEventChan:  make(chan struct{}),
		shutdownNotifyChan: make(chan struct{}),
	}
}

// Wait will block the current goroutine until the shutdown process is finished.
// it will listen to the Signals and Context if they are provided.
// once any of them is triggered, the graceful shutdown process will be performed.
// if the ShutdownKeeper is already in shutdown status, Wait will return immediately.
func (k *ShutdownKeeper) Wait() {
	if !k.tryRun() {
		return
	}

	go func() {
		<-k.shutdownEventChan
		defer func() { _ = recover() }()
		close(k.shutdownNotifyChan)
	}()

	if len(k.signals) == 0 && k.ctx == nil {
		k.startShutdown(nil)
	} else {
		go k.listenSignals()
		go k.listenContext()
	}
	<-k.shutdownEventChan

	if !k.alwaysHold && k.getHoldTokenNum() == 0 {
		return
	}
	select {
	case <-time.After(k.maxHoldTime):
	case <-k.tokenReleaseChan:
	}
}

// AllocHoldToken allocates a hold token.
func (k *ShutdownKeeper) AllocHoldToken() HoldToken {
	atomic.AddInt32(&k.holdTokenNum, 1)
	k.tokenGroup.Add(1)
	return &holdTokenImpl{
		releaseLock: &sync.Mutex{},
		releasedFunc: func() {
			k.tokenGroup.Done()
			atomic.AddInt32(&k.holdTokenNum, -1)
			if atomic.LoadInt32(&k.status) == keeperShutdown && k.getHoldTokenNum() == 0 && len(k.tokenReleaseChan) == 0 {
				close(k.tokenReleaseChan)
			}
		},
		shutdownNotifyChan: k.shutdownNotifyChan,
	}
}

// getHoldTokenNum returns the number of hold tokens that are not released yet.
func (k *ShutdownKeeper) getHoldTokenNum() int {
	return int(atomic.LoadInt32(&k.holdTokenNum))
}

func (k *ShutdownKeeper) listenSignals() {
	if len(k.signals) == 0 {
		return
	}

	signal.Notify(k.signalChan, k.signals...)
	s := <-k.signalChan
	k.startShutdown(func(s os.Signal) func() {
		return func() {
			if k.signalHandler != nil {
				k.signalHandler(s)
			}
		}
	}(s))
}

func (k *ShutdownKeeper) listenContext() {
	if k.ctx == nil {
		return
	}

	<-k.ctx.Done()
	k.startShutdown(k.ctxHandler)
}

func (k *ShutdownKeeper) tryRun() bool {
	return atomic.CompareAndSwapInt32(&k.status, keeperInit, keeperRunning)
}

func (k *ShutdownKeeper) startShutdown(eventCallbackFunc func()) bool {
	if atomic.CompareAndSwapInt32(&k.status, keeperRunning, keeperShutdown) {
		if eventCallbackFunc != nil {
			eventCallbackFunc()
		}
		close(k.shutdownEventChan)
		return true
	}
	return false
}
