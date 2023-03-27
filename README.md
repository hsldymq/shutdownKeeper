# Shutdown Keeper

[![Go Report Card](https://goreportcard.com/badge/github.com/hsldymq/shutdownKeeper)](https://goreportcard.com/report/github.com/hsldymq/shutdownKeeper)
[![Test](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml/badge.svg)](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/hsldymq/shutdownKeeper/branch/main/graph/badge.svg?token=JWHQP7XRMV)](https://codecov.io/gh/hsldymq/shutdownKeeper)

---

This tiny package try to help you to better control your program's shutdown process.

It provides an easy way to perform graceful shutdown, by notifying subroutines with shutdown signal, to let them finish their work.

## Installation

```bash
go get github.com/hsldymq/shutdownKeeper
```

## Basic Usage
The example below shows a basic usage of Shutdown Keeper of performing graceful shutdown for a http service.

```go
package main

import (
	"context"
	"github.com/hsldymq/shutdownKeeper"
	"net/http"
	"os"
	"syscall"
	"time"
)

func main() {
	// we configure the shutdownKeeper to listen to SIGINT and SIGTERM signals
	// and give a maximum of 20 seconds for the service to perform graceful shutdown
	keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
		Signals:     []os.Signal{syscall.SIGINT, syscall.SIGTERM},
		MaxHoldTime: 20 * time.Second,
	})

	go func() {
		server := &http.Server{
			Addr: ":8011",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// ...
			}),
		}
		go func(token shutdownKeeper.HoldToken) {
			// HoldToken is used to listen to the shutdown event and perform graceful shutdown.
			// ListenShutdown() will block until the service receives a SIGINT or SIGTERM signal.
			<-token.ListenShutdown()

			server.Shutdown(context.Background())

			// Release the HoldToken when http server is finally shutdown.
			// then keeper.Wait() will return.
			token.Release()
		}(keeper.AllocHoldToken())
		_ = server.ListenAndServe()
	}()

	// Wait will block until the main goroutine.
	// When service receives a SIGINT or SIGTERM signal, it will keep blocking until every HoldToken is released or the MaxHoldTime is reached.
	keeper.Wait()
}
```

## Shutdown by Context.Done event
Assume that we have a long-running task, and we accept http request shutdown the task.

Shutdown keeper can also listen to the Context.Done event, and perform graceful shutdown.

```go
package main

import (
	"context"
	"github.com/hsldymq/shutdownKeeper"
	"net/http"
	"time"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
		Context:     ctx,
		MaxHoldTime: 20 * time.Second,
	})

	go func(token shutdownKeeper.HoldToken) {
		// RunTask is used to run a task that may block this goroutine until the context is canceled.
		RunTask(ctx)
		token.Release()
	}(keeper.AllocHoldToken())

	go func() {
		server := &http.Server{
			Addr: ":8011",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && r.URL.Path == "/shutdown" {
					cancel()
					return
				}
				// ...
			}),
		}
		go func(token shutdownKeeper.HoldToken) {
			// ListenShutdown will block until the context is canceled.
			<-token.ListenShutdown()
			server.Shutdown(context.Background())
			token.Release()
		}(keeper.AllocHoldToken())
		_ = server.ListenAndServe()
	}()

	keeper.Wait()
}
```