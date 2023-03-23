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

	status              int32
	shutdownProcessChan chan struct{}
	shutdownNotifyChan  chan struct{}
	l                   *sync.Mutex
}

func NewShutdownKeeper(opts KeeperOpts) *ShutdownKeeper {
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
		tokenReleaseChan: make(chan struct{}, 1),

		status:              keeperInit,
		shutdownProcessChan: make(chan struct{}, 1),

		l: &sync.Mutex{},
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
	<-k.shutdownProcessChan

	if !k.alwaysHold && k.HoldTokenNum() == 0 {
		return
	}
	select {
	case <-time.After(k.maxHoldTime):
	case <-k.tokenReleaseChan:
	}
}

// ListenShutdown returns a channel to notify callers that the shutdown process is triggered.
// This is sometimes useful when you have multiple subroutines, and any of them wants to know if shutdown process is triggered by others.
func (k *ShutdownKeeper) ListenShutdown() <-chan struct{} {
	k.l.Lock()
	defer k.l.Unlock()
	if k.shutdownNotifyChan == nil {
		k.shutdownNotifyChan = make(chan struct{}, 1)
		go func() {
			for {
				if len(k.shutdownNotifyChan) == 0 {
					k.shutdownNotifyChan <- struct{}{}
					continue
				}
				time.Sleep(time.Second)
			}
		}()
	}

	return k.shutdownNotifyChan
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
			if atomic.LoadInt32(&k.status) == keeperShutdown && k.HoldTokenNum() == 0 && len(k.tokenReleaseChan) == 0 {
				k.tokenReleaseChan <- struct{}{}
			}
		},
	}
}

// HoldTokenNum returns the number of hold tokens that are not released yet.
func (k *ShutdownKeeper) HoldTokenNum() int {
	return int(atomic.LoadInt32(&k.holdTokenNum))
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
	return atomic.CompareAndSwapInt32(&k.status, keeperInit, keeperRunning)
}

func (k *ShutdownKeeper) startShutdown() bool {
	if atomic.CompareAndSwapInt32(&k.status, keeperRunning, keeperShutdown) {
		k.shutdownProcessChan <- struct{}{}
		return true
	}
	return false
}
