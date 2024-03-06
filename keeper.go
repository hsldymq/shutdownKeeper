package shutdownKeeper

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

// HoldToken is used by subroutines to listen for shutdown events and allows subroutines to complete their work.
// Each subroutine holding a HoldToken should call the Release() method when its work is finished.
// Once all HoldTokens are released, the shutdown keeper will return from its Wait() method call.
type HoldToken interface {
	// ListenShutdown will block the current goroutine until the shutdown stage is triggered.
	ListenShutdown()

	// HoldChan returns a channel that will be closed when the shutdown stage is triggered.
	HoldChan() <-chan struct{}

	Release()
}

type holdTokenImpl struct {
	released     bool
	releasedFunc func()
	releaseLock  *sync.Mutex

	shutdownNotifyChan <-chan struct{}
}

func (kt *holdTokenImpl) ListenShutdown() {
	<-kt.shutdownNotifyChan
}

func (kt *holdTokenImpl) HoldChan() <-chan struct{} {
	return kt.shutdownNotifyChan
}

func (kt *holdTokenImpl) Release() {
	kt.releaseLock.Lock()
	defer kt.releaseLock.Unlock()
	if !kt.released {
		kt.released = true
		go kt.releasedFunc()
	}
}

const (
	keeperInit int32 = iota
	keeperRunning
	keeperShutdown
)

// KeeperOpts contains options for creating a ShutdownKeeper.
type KeeperOpts struct {
	// Signals specifies the signals that ShutdownKeeper will listen to.
	// Any signal in this slice will trigger the shutdown process.
	Signals []os.Signal

	// Context listens to the context.Done() event. This event will trigger the shutdown process.
	Context context.Context

	// OnSignalShutdown is called when ShutdownKeeper receives any signal provided by Signals.
	// This allows you to perform a graceful shutdown (like stop running subroutines) before the program actually shuts down.
	// If both signal and context.Done() are triggered,
	// only one of OnSignalShutdown and OnContextDone will be called, depending on which event is triggered first.
	OnSignalShutdown func(os.Signal)

	// OnContextDone is called when ShutdownKeeper receives a context.Done() event.
	// If both signal and context.Done() are triggered,
	// only one of OnSignalShutdown and OnContextDone will be called, depending on which event is triggered first.
	OnContextDone func()

	// MaxHoldTime is the maximum time that ShutdownKeeper will wait for all HoldTokens to be released.
	// If the time is exceeded, ShutdownKeeper.Wait() will force return.
	MaxHoldTime time.Duration

	// If AlwaysHold is true, ShutdownKeeper will always hold the shutdown process for MaxHoldTime, even if no HoldToken is allocated.
	AlwaysHold bool
}

// ShutdownKeeper manages the graceful shutdown process of a program.
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
	maxHoldTime := opts.MaxHoldTime
	if maxHoldTime == 0 {
		maxHoldTime = 30 * time.Second
	}

	return &ShutdownKeeper{
		signals:       opts.Signals,
		signalHandler: opts.OnSignalShutdown,
		signalChan:    make(chan os.Signal, 1),

		ctx:        opts.Context,
		ctxHandler: opts.OnContextDone,

		maxHoldTime:      maxHoldTime,
		holdTokenNum:     0,
		alwaysHold:       opts.AlwaysHold,
		tokenGroup:       &sync.WaitGroup{},
		tokenReleaseChan: make(chan struct{}),

		status:             keeperInit,
		shutdownEventChan:  make(chan struct{}),
		shutdownNotifyChan: make(chan struct{}),
	}
}

// Wait blocks the current goroutine until the shutdown process is finished.
// It listens to Signals and Context if provided.
// Once any of them is triggered, the graceful shutdown process will be performed.
// If the ShutdownKeeper is already in shutdown status, Wait will return immediately.
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

	if k.alwaysHold {
		<-time.After(k.maxHoldTime)
	} else if k.getHoldTokenNum() > 0 {
		select {
		case <-time.After(k.maxHoldTime):
		case <-k.tokenReleaseChan:
		}
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
			remainTokenNum := atomic.AddInt32(&k.holdTokenNum, -1)
			if atomic.LoadInt32(&k.status) == keeperShutdown && remainTokenNum == 0 && len(k.tokenReleaseChan) == 0 {
				close(k.tokenReleaseChan)
			}
		},
		shutdownNotifyChan: k.shutdownNotifyChan,
	}
}

// OnShuttingDown registers a function to be called when the shutdown process is triggered.
func (k *ShutdownKeeper) OnShuttingDown(f func()) {
	if k.status == keeperShutdown {
		return
	}

	go func(token HoldToken) {
		defer token.Release()
		token.ListenShutdown()
		f()
	}(k.AllocHoldToken())
}

// getHoldTokenNum returns the number of hold tokens that have not been released yet.
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
