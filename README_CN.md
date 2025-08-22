# Shutdown Keeper

[![Go Report Card](https://goreportcard.com/badge/github.com/hsldymq/shutdownKeeper)](https://goreportcard.com/report/github.com/hsldymq/shutdownKeeper)
[![Test](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml/badge.svg)](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/hsldymq/shutdownKeeper/branch/main/graph/badge.svg?token=JWHQP7XRMV)](https://codecov.io/gh/hsldymq/shutdownKeeper)

---

这个库用于帮助你实现程序的优雅退出,让每个子模块在程序关闭前都有机会完成手头的工作,避免数据丢失和状态不一致.

## 核心概念

Shutdown Keeper 通过两个核心概念解决这些问题:

### ShutdownKeeper
应用程序关闭流程的协调者, 负责:
- 监听系统信号或其他关闭事件
- 管理所有子模块的关闭流程
- 确保所有模块都有足够时间完成收尾工作

### HoldToken
分配给每个子模块的"关闭令牌",子模块通过它：
- 监听关闭事件
- 执行收尾工作
- 通知 Keeper 自己已完成关闭

### 安装

```bash
go get github.com/hsldymq/shutdownKeeper/v2
```

### 基础示例：优雅关闭 HTTP 服务

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

// 在这个应用中, 你可以在请求接口的过程中按下 Ctrl+C 或发送 SIGTERM 信号来测试优雅关闭的效果, 程序会等待接口处理完成后再退出.
func main() {
    // 创建 ShutdownKeeper,监听 SIGINT 和 SIGTERM 信号
    keeper := v2.NewKeeper(v2.KeeperOpts{
        Signals:     []os.Signal{syscall.SIGINT, syscall.SIGTERM},
        MaxHoldTime: 60 * time.Second, // 优雅退出过程最多等待 60 秒
    })

    // 启动 HTTP 服务
    server := &http.Server{
        Addr: ":8080",
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 模拟耗时操作
            time.Sleep(5 * time.Second)
            w.Write([]byte("Hello, World!"))
        }),
    }

    // GoWaitAndRun 会启动一个goroutine来监听程序的关闭事件
    // 当收到事件后,会尽可能确保http server完成关闭后整个程序才安全退出
    keeper.AllocHoldToken().GoWaitAndRun(func() {
        // 当收到关闭信号时会执行
        server.Shutdown(context.Background())
    })

    fmt.Println("HTTP 服务启动在端口 8080")
    go server.ListenAndServe()

    fmt.Println("应用程序已启动,按 Ctrl+C 优雅退出")
    keeper.Wait() // 阻塞直到收到关闭信号且所有清理工作完成, 或者等待时间超过 MaxHoldTime
    fmt.Println("应用程序已优雅退出")
}
```

## 使用场景

### 场景 1：数据库操作的优雅关闭

```go
func runDatabaseWorker(db Database, token v2.HoldToken) {
    defer token.Release() // 确保最终释放 token
    for {
        select {
        case job := <-jobQueue:
            // 处理数据库任务
            processJob(db, job)
        case <-token.Context().Done():
            // 收到关闭信号,完成当前事务后退出
            fmt.Println("数据库工作器收到关闭信号,正在完成当前事务...")
            finishCurrentTransaction(db)
            fmt.Println("数据库工作器已安全关闭")
            db.Close()
            return
        }
    }
}
```

### 场景 2：消息队列消费者的优雅关闭

```go
func runMessageConsuming(consumer Consumer, token v2.HoldToken) {
    go func() {
        defer token.Release()
        token.ListenShutdown()  // 阻塞监听关闭事件
        
        defer consumer.Close()
        
        fmt.Println("消息消费者收到关闭信号,停止接收新消息...")
        consumer.StopReceiving()
        
        // 处理完已接收的消息
        consumer.ProcessRemainingMessages()
        fmt.Println("消息消费者已安全关闭")
    }()
    consumer.StartConsuming() // 阻塞消费消息
    
    ///////////////////////////////////////////////////////////
    
    // 下面的代码是一个简化版本, 它使用 GoWaitAndRun 简化上面监听和释放token的逻辑
    token.GoWaitAndRun(func() {
        defer consumer.Close()
        fmt.Println("消息消费者收到关闭信号,停止接收新消息...")
        consumer.StopReceiving()
        
        // 处理完已接收的消息
        consumer.ProcessRemainingMessages()
        fmt.Println("消息消费者已安全关闭")
    })
    consumer.StartConsuming() // 阻塞消费消息
}
```

### 场景 3：多个子模块协调关闭

```go
package main

import (
    "fmt"
    "time"

    "github.com/hsldymq/shutdownKeeper/v2"
)

func main() {
    keeper := v2.NewKeeper(v2.KeeperOpts{
        Signals:     []os.Signal{syscall.SIGINT, syscall.SIGTERM},
        MaxHoldTime: 60 * time.Second,
    })

    // 启动多个子模块, 为每个模块都分配一个 HoldToken, 让它们都有机会完成收尾工作
    go runHTTPServer(keeper.AllocHoldToken())
    go runDatabaseWorker(keeper.AllocHoldToken())
    go runMessageConsumer(keeper.AllocHoldToken())
    go runFileUploader(keeper.AllocHoldToken())
    go runCacheManager(keeper.AllocHoldToken())

    fmt.Println("所有服务已启动")
    keeper.Wait() // 等待所有子模块完成关闭
    fmt.Println("所有服务已优雅关闭")
}
```

### 场景 4：任务完成后自动退出

```go
package main

import (
    "fmt"
    "time"

    "github.com/hsldymq/shutdownKeeper/v2"
)

func main() {
    // 使用 ShutdownWhenNoTokens 模式
    keeper := v2.NewKeeper(v2.KeeperOpts{
        TokenReleaseMode: v2.ShutdownWhenNoTokens, // 当所有 token 释放后自动关闭
    })

    // 启动一些临时任务
    for i := 0; i < 5; i++ {
        taskID := i
        // GoRun 是一个快捷方式, 它会在函数执行完成后自动释放HoldToken
        keeper.AllocHoldToken().GoRun(func() {
            fmt.Printf("任务 %d 开始执行\n", taskID)
            time.Sleep(time.Duration(taskID+1) * time.Second)
            fmt.Printf("任务 %d 执行完成\n", taskID)
        })
    }

    keeper.Wait() // 等待所有任务完成后自动退出
    fmt.Println("所有任务已完成,程序退出")
}
```

## 其他功能

### 自定义信号处理

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
    keeper := v2.NewKeeper(v2.KeeperOpts{
        // 如果没有注册 OnSignal 函数, 当收到信号时, Keeper 会自动启动退出程序开始优雅关闭流程
        Signals: []os.Signal{syscall.SIGINT},
        // 但如果注册了 OnSignal 函数, 那么该函数就应该负责程序退出的决策, 这样你可以实现一些特殊的退出逻辑
        // 比如在这个例子中, 只有当你按下3次 Ctrl+C 时才会退出程序
        OnSignal: func(sig os.Signal, shutdown v2.ShutdownFunc) {
            signalCount++
            fmt.Printf("收到 SIGINT %d次\n", signalCount)
            if signalCount >= 3 {
                fmt.Println("开始关闭")
                shutdown()
            }
        },
    })

    keeper.Wait()
}
```

### 强制等待最大时间

```go
keeper := v2.NewKeeper(v2.KeeperOpts{
    MaxHoldTime:       10 * time.Second,
    AlwaysHoldMaxTime: true, // 即使所有 token 都释放了,也要保证等待 10 秒后再退出
})
```

## 常见问题

### Q: 为什么不直接使用 context.WithCancel?

A: Context 只能传递取消信号,但不能确保所有 goroutine 都完成了清理工作.Shutdown Keeper 通过 HoldToken 机制确保每个子模块都有机会完成收尾工作.

### Q: 如果某个模块一直不释放 Token 怎么办?

A: 设置 `MaxHoldTime` 参数,超时后会强制退出.你也可以在每个模块内部设置自己的超时逻辑.

### Q: 可以在运行时动态分配 Token 吗?

A: 可以！你可以随时调用 `AllocHoldToken()`,Keeper 会跟踪所有分配的 token.