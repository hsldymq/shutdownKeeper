# Shutdown Keeper

[![Go Report Card](https://goreportcard.com/badge/github.com/hsldymq/shutdownKeeper)](https://goreportcard.com/report/github.com/hsldymq/shutdownKeeper)
[![Test](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml/badge.svg)](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/hsldymq/shutdownKeeper/branch/main/graph/badge.svg?token=JWHQP7XRMV)](https://codecov.io/gh/hsldymq/shutdownKeeper)

---

This package is designed to help you better control your program's shutdown process. 

Its main goal is to provide you with a simple way to gracefully shut down your program.

## Installation

```bash
go get github.com/hsldymq/shutdownKeeper
```

## Example 1: Graceful shutdown of an HTTP service
This example below demonstrates the basic usage of Shutdown Keeper for performing a graceful shutdown of an HTTP service.

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
	// Configure the shutdownKeeper to listen for SIGINT and SIGTERM signals,
	// and allow a maximum of 20 seconds for the service to perform a graceful shutdown
	keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
		Signals:     []os.Signal{syscall.SIGINT, syscall.SIGTERM},
		MaxHoldTime: 20 * time.Second,  // default is 30 seconds
	})

	go func() {
		server := &http.Server{
			Addr: ":8011",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// ...
			}),
		}

        	// When the service receives a SIGINT or SIGTERM signal, shutdown keeper will not terminate the program immediately,
        	// instead, it will call registered functions to do the cleanup work,  like close the HTTP server, release resources, etc.
		keeper.OnShuttingDown(func() { server.Shutdown(context.Background()) })
		// Or you can use the following code to achieve the same effect:
		/*
		    go func(token shutdownKeeper.HoldToken) {
			    // Release the HoldToken when the HTTP server is finally shut down.
			    // Then the program will return.
			    defer token.Release()
			
			    // ListenShutdown() will block until the service receives a SIGINT or SIGTERM signal.
			    token.ListenShutdown()

			    server.Shutdown(context.Background())
		    }(keeper.AllocHoldToken()) // HoldToken is used to listen to the shutdown event and perform a graceful shutdown.
		 */
		
		_ = server.ListenAndServe()
	}()

	// Wait will block the main goroutine.
	// When the service receives a SIGINT or SIGTERM signal, it will keep blocking until every HoldToken is released or the MaxHoldTime is reached.
	keeper.Wait()
}
```

## Example 2: Shutdown by Context.Done event
Suppose we have a long-running task, and we accept an HTTP request to shut down the task.

Shutdown Keeper can also listen to the Context.Done event and perform a graceful shutdown.

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

	// AllocHoldToken allocates a HoldToken, the shutdown keeper tracks every tokens' status that it allocates.
	// once shutdown process is triggered, shutdown keeper will wait for every token to be released.
	go func(token shutdownKeeper.HoldToken) {
		defer token.Release()

        	// RunTask is used to run a task that may block this goroutine until the context is canceled.
        	RunTask(token.Context())
	}(keeper.AllocHoldToken())

	server := &http.Server{
		Addr: ":8011",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/shutdown" {
                // after cancel function is called, shutdown keeper will start the graceful shutdown process.
				cancel()
				return
			}
			// ...
		}),
	}
	keeper.OnShuttingDown(func() { server.Shutdown(context.Background()) })
	go server.ListenAndServe()

	keeper.Wait()
}
```
