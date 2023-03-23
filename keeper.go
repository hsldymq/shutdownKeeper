package shutdownKeeper

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"
)

// HoldToken is a token that can be used to hold the shutdown process to let the subroutines finish their work.
// once the all HoldTokens are released, the shutdown process will be continued.
type HoldToken interface {
	Release()
}

type holdTokenImpl struct {
	released     bool
	releasedFunc func()
	releaseLock  *sync.Mutex
}

func (kt *holdTokenImpl) Release() {
	kt.releaseLock.Lock()
	defer kt.releaseLock.Unlock()
	if !kt.released {
		kt.released = true
		go kt.releasedFunc()
	}
}

type keeperStatus int

const (
	keeperInit keeperStatus = iota
	keeperRunning
	keeperShutdown
)

// KeeperOpts is the options for creating ShutdownKeeper.
type KeeperOpts struct {
	// Signals determines the signals that ShutdownKeeper will listen to.
	// any signal in this slice will trigger the shutdown process.
	Signals []os.Signal

	// Context allows ShutdownKeeper shutdown from listening to the context.Done() event.
	Context context.Context

	// OnSignalShutdown that will be called when ShutdownKeeper receives any signal provided by Signals.
	// this allows you to perform graceful shutdown by stopping the process before the program is actually shutdown.
	OnSignalShutdown func(os.Signal)

	// OnContextDone will be called when ShutdownKeeper receives a context.Done() event.
	OnContextDone func()

	AlwaysHold  bool
	MaxHoldTime time.Duration
}

type ShutdownKeeper struct {
	signals       []os.Signal
	signalHandler func(os.Signal)
	signalChan    chan os.Signal

	ctx        context.Context
	ctxHandler func()

	maxHoldTime      time.Duration
	holdTokenNum     *atomic.Int32
	alwaysHold       bool
	tokenGroup       *sync.WaitGroup
	tokenReleaseChan chan struct{}

	status       keeperStatus
	statusLocker *sync.Mutex
	shutdownChan chan struct{}
}

func NewShutdownKeeper(opts KeeperOpts) *ShutdownKeeper {
	return &ShutdownKeeper{
		signals:       opts.Signals,
		signalHandler: opts.OnSignalShutdown,
		signalChan:    make(chan os.Signal, 1),

		ctx:        opts.Context,
		ctxHandler: opts.OnContextDone,

		maxHoldTime:      opts.MaxHoldTime,
		holdTokenNum:     &atomic.Int32{},
		alwaysHold:       opts.AlwaysHold,
		tokenGroup:       &sync.WaitGroup{},
		tokenReleaseChan: make(chan struct{}, 1),

		status:       keeperInit,
		statusLocker: &sync.Mutex{},
		shutdownChan: make(chan struct{}, 1),
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

	if len(k.signals) == 0 && k.ctx == nil {
		k.startShutdown()
	} else {
		go k.listenSignals()
		go k.listenContext()
	}
	<-k.shutdownChan

	if !k.alwaysHold && k.HoldTokenNum() == 0 {
		return
	}
	select {
	case <-time.After(k.maxHoldTime):
	case <-k.tokenReleaseChan:
	}
}

// AllocHoldToken allocates a hold token.
func (k *ShutdownKeeper) AllocHoldToken() HoldToken {
	k.holdTokenNum.Add(1)
	k.tokenGroup.Add(1)
	return &holdTokenImpl{
		releaseLock: &sync.Mutex{},
		releasedFunc: func() {
			k.tokenGroup.Done()
			k.holdTokenNum.Add(-1)
			if k.status == keeperShutdown && k.holdTokenNum.Load() == 0 && len(k.tokenReleaseChan) == 0 {
				k.tokenReleaseChan <- struct{}{}
			}
		},
	}
}

// HoldTokenNum returns the number of hold tokens that are not released yet.
func (k *ShutdownKeeper) HoldTokenNum() int {
	return int(k.holdTokenNum.Load())
}

func (k *ShutdownKeeper) listenSignals() {
	if len(k.signals) == 0 {
		return
	}

	signal.Notify(k.signalChan, k.signals...)
	select {
	case s := <-k.signalChan:
		if !k.startShutdown() {
			return
		}
		if k.signalHandler != nil {
			k.signalHandler(s)
		}
	}
}

func (k *ShutdownKeeper) listenContext() {
	if k.ctx == nil {
		return
	}

	select {
	case <-k.ctx.Done():
		if !k.startShutdown() {
			return
		}
		if k.ctxHandler != nil {
			k.ctxHandler()
		}
	}
}

func (k *ShutdownKeeper) tryRun() bool {
	k.statusLocker.Lock()
	defer k.statusLocker.Unlock()
	if k.status != keeperInit {
		return false
	}
	k.status = keeperRunning
	return true
}

func (k *ShutdownKeeper) startShutdown() bool {
	k.statusLocker.Lock()
	defer k.statusLocker.Unlock()
	if k.status != keeperRunning {
		return false
	}
	k.status = keeperShutdown
	k.shutdownChan <- struct{}{}
	return true
}