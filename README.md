# Shutdown Keeper

[![Go Report Card](https://goreportcard.com/badge/github.com/hsldymq/shutdownKeeper)](https://goreportcard.com/report/github.com/hsldymq/shutdownKeeper)
[![Test](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml/badge.svg)](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/hsldymq/shutdownKeeper/branch/main/graph/badge.svg?token=JWHQP7XRMV)](https://codecov.io/gh/hsldymq/shutdownKeeper)

---

[中文](./README_CN.md)

This library helps you implement graceful application shutdown. It provides a coordination manager for your application. The manager enables bidirectional status synchronization between the program and its sub-modules during shutdown.

Modules can sense shutdown signals and perform cleanup work. They can also notify the coordination manager when cleanup tasks are completed. This approach prevents data loss and inconsistency. It also avoids excessive waiting time.

### Installation

```bash
go get github.com/hsldymq/shutdownKeeper/v2
```

### Basic Example: Graceful HTTP Service Shutdown

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    "os"
    "syscall"
    "time"

    "github.com/hsldymq/shutdownKeeper/v2"
)

// In this application, you can press Ctrl+C or send a SIGTERM signal during API request processing to test the graceful shutdown effect. The program will wait for the API processing to complete before exiting.
func main() {
    // Create ShutdownKeeper, listening for SIGINT and SIGTERM signals
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
        Signals:     []os.Signal{syscall.SIGINT, syscall.SIGTERM},
        MaxHoldTime: 60 * time.Second, // Wait at most 60 seconds during graceful shutdown
    })

    // Start HTTP service
    server := &http.Server{
        Addr: ":8080",
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Simulate time-consuming operation
            time.Sleep(5 * time.Second)
            w.Write([]byte("Hello, World!"))
        }),
    }

    // Use AllocHoldToken to allocate a HoldToken
    // HoldToken plays an important role throughout the graceful shutdown process
    // It can sense the program's shutdown signal and notify ShutdownKeeper after completing cleanup work
    token := keeper.AllocHoldToken() 
    go func(token shutdownKeeper.HoldToken) {
        defer token.Release()   // Ensure final release of token, so ShutdownKeeper can sense that this module has completed cleanup
        token.ListenShutdown()  // Block here until shutdown signal is received
        
        server.Shutdown(token.DeadlineContext())
    }(token)

    // The above code has an equivalent shortcut, namely the following code:
    keeper.AllocHoldToken().DoOnShutdown(func(deadlineCtx context.Context) {
        // Executes when shutdown signal is received
        server.Shutdown(deadlineCtx)
    })

    fmt.Println("HTTP service started on port 8080")
    go server.ListenAndServe()

    fmt.Println("Application started, press Ctrl+C for graceful shutdown")
    keeper.Wait() // Block until shutdown signal is received and all cleanup work is completed, or waiting time exceeds MaxHoldTime
    fmt.Println("Application gracefully shut down")
}
```

## Use Cases

### Scenario 1: Graceful Database Operation Shutdown

```go
func runDatabaseWorker(db Database, token shutdownKeeper.HoldToken) {
    defer token.Release()
    for {
        select {
        case job := <-jobQueue:
            // Process database task
            processJob(db, job)
        case <-token.Context().Done():
            // Received shutdown signal, finish current transaction then exit
            fmt.Println("Database worker received shutdown signal, finishing current transaction...")
            finishCurrentTransaction(db)
            fmt.Println("Database worker safely shut down")
            db.Close()
            return
        }
    }
}
```

### Scenario 2: Graceful Message Queue Consumer Shutdown

```go
func runMessageConsuming(consumer Consumer, token shutdownKeeper.HoldToken) {    
    token.DoOnShutdown(func(deadlineCtx context.Context) {
        defer consumer.Close()
        
        fmt.Println("Message consumer received shutdown signal, stopping new message reception...")
        consumer.StopReceiving(deadlineCtx)
        
        // Process remaining received messages
        consumer.ProcessRemainingMessages(deadlineCtx)
        fmt.Println("Message consumer safely shut down")
    })
    consumer.StartConsuming() // Block consuming messages
}
```

### Scenario 3: Automatic Exit After Task Completion

```go
package main

import (
    "fmt"
    "time"

    "github.com/hsldymq/shutdownKeeper/v2"
)

func main() {
    // Use ShutdownWhenNoTokens mode
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
        // In this model, typically used for executing short-term tasks, when all tasks are completed, the program will automatically exit
        TokenReleaseMode: shutdownKeeper.ShutdownWhenNoTokens, 
    })

    for i := 0; i < 5; i++ {
        // GoRun is a shortcut method that automatically releases HoldToken after function execution completes
        keeper.AllocHoldToken().GoRun(func() {
            fmt.Printf("Task %d started\n", i+1)
            time.Sleep(time.Duration(i+1) * time.Second)
            fmt.Printf("Task %d completed\n", i+1)
        })

        // Additionally, GoRun also has a version with a context parameter called GoRunWithCtx. You can use the context to sense shutdown events, giving you the opportunity to interrupt task execution.
    }

    keeper.Wait() // Wait for all tasks to complete then automatically exit
    fmt.Println("All tasks completed, program exiting")
}
```

## Additional Features

### Custom Signal Handling

```go
package main

import (
    "fmt"
    "os"
    "syscall"

    "github.com/hsldymq/shutdownKeeper/v2"
)

func main() {
    signalCount := 0
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
        // If no OnSignal function is registered, when a signal is received, Keeper will automatically start the program exit and begin graceful shutdown process
        Signals: []os.Signal{syscall.SIGINT},
        // But once an OnSignal function is registered, that function should be responsible for program exit decisions, allowing you to implement special exit logic
        // For example, in this example, the program will only exit when you press Ctrl+C 3 times
        OnSignal: func(sig os.Signal, shutdown shutdownKeeper.ShutdownFunc) {
            signalCount++
            fmt.Printf("Received SIGINT %d times\n", signalCount)
            if signalCount >= 3 {
                fmt.Println("Shutting down program")
                shutdown()
            }
        },
    })

    keeper.Wait()
}
```

### Force Maximum Wait Time

```go
keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
    MaxHoldTime:       10 * time.Second,
    AlwaysHoldMaxTime: true, // Even if all tokens are released, ensure waiting for 10 seconds before exiting
})
```

## Frequently Asked Questions

### Q: Why not use context.WithCancel directly?

A: Context can only pass cancellation signals but cannot ensure that all goroutines have completed cleanup work. Shutdown Keeper ensures each sub-module has the opportunity to complete cleanup work through the HoldToken mechanism.

### Q: What if a module never releases its Token?

A: Set the `MaxHoldTime` parameter, and it will force exit after timeout. You can also set your own timeout logic within each module.

### Q: Can Tokens be dynamically allocated at runtime?

A: Yes! You can call `AllocHoldToken()` at any time, and Keeper will track all allocated tokens.