# Shutdown Keeper

[![Go Report Card](https://goreportcard.com/badge/github.com/hsldymq/shutdownKeeper)](https://goreportcard.com/report/github.com/hsldymq/shutdownKeeper)
[![Test](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml/badge.svg)](https://github.com/hsldymq/shutdownKeeper/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/hsldymq/shutdownKeeper/branch/main/graph/badge.svg?token=JWHQP7XRMV)](https://codecov.io/gh/hsldymq/shutdownKeeper)

---

这个库用于帮助你实现程序的优雅退出. 它提供一个协调管理器, 使程序和各个子模块之间在关闭过程中能够双向同步状态. 

各个模块可以感知到程序的关闭信号, 从而进行收尾工作. 模块也能够通知协调管理器自己已经完成了收尾工作. 这样既能最大限度避免数据丢失和不一致, 也不必过度等待太长时间. 

### 安装

```bash
go get github.com/hsldymq/shutdownKeeper/v2
```

### 基础示例: 优雅关闭 HTTP 服务

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
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
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

    // 使用 AllocHoldToken 分配一个 HoldToken
    // HoldToken 在整个优雅关闭过程里扮演重要角色
    // 它即能够感知到程序的关闭信号, 也能够在完成收尾工作后通知 ShutdownKeeper
    token := keeper.AllocHoldToken() 
    go func(token shutdownKeeper.HoldToken) {
        defer token.Release()   // 确保最终释放 token, 这样ShutdownKeeper就能够感知到该模块已经完成了收尾工作
        token.ListenShutdown()  // 阻塞在这里, 直到收到关闭信号
        
        server.Shutdown(token.DeadlineContext())
    }(token)

    // 上面的代码还有一个等价的快捷方式, 即如下代码:
    keeper.AllocHoldToken().DoOnShutdown(func(deadlineCtx context.Context) {
        // 当收到关闭信号时会执行
        server.Shutdown(deadlineCtx)
    })

    fmt.Println("HTTP 服务启动在端口 8080")
    go server.ListenAndServe()

    fmt.Println("应用程序已启动,按 Ctrl+C 优雅退出")
    keeper.Wait() // 阻塞直到收到关闭信号且所有清理工作完成, 或者等待时间超过 MaxHoldTime
    fmt.Println("应用程序已优雅退出")
}
```

## 使用场景

### 场景 1: 数据库操作的优雅关闭

```go
func runDatabaseWorker(db Database, token shutdownKeeper.HoldToken) {
    defer token.Release()
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

### 场景 2: 消息队列消费者的优雅关闭

```go
func runMessageConsuming(consumer Consumer, token shutdownKeeper.HoldToken) {
    token.DoOnShutdown(func(deadlineCtx context.Context) {
        defer consumer.Close()
        
        fmt.Println("消息消费者收到关闭信号,停止接收新消息...")
        consumer.StopReceiving(deadlineCtx)
        
        // 处理完已接收的消息
        consumer.ProcessRemainingMessages(deadlineCtx)
        fmt.Println("消息消费者已安全关闭")
    })
    consumer.StartConsuming() // 阻塞消费消息
}
```

### 场景 3: 任务完成后自动退出

```go
package main

import (
    "context"
    "fmt"
    "time"
    "os"
    "syscall"

    "github.com/hsldymq/shutdownKeeper/v2"
)

func main() {
    // 使用 ShutdownWhenNoTokens 模式, 在这种模型下, 通常用于执行一些短期任务, 当所有任务执行完成后, 程序就会自动退出 
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
        TokenReleaseMode: shutdownKeeper.ShutdownWhenNoTokens,
    })

    for i := 0; i < 5; i++ {
        // GoRun 是一个快捷方法, 它会在函数执行完成后自动释放HoldToken
        keeper.AllocHoldToken().GoRun(func() {
            fmt.Printf("任务 %d 开始执行\n", i+1)
            time.Sleep(time.Duration(i+1) * time.Second)
            fmt.Printf("任务 %d 执行完成\n", i+1)
        })
        
        // 另外, GoRun 还有一个携带 context 参数的版本 GoRunWithCtx, 你可以通过 context 来感知关闭事件, 这样有机会打断任务的执行
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
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
        // 如果没有注册 OnSignal 函数, 当收到信号时, Keeper 会自动启动退出程序开始优雅关闭流程
        Signals: []os.Signal{syscall.SIGINT},
        // 但一旦注册了 OnSignal 函数, 那么该函数就应该负责程序退出的决策, 这样你可以实现一些特殊的退出逻辑
        // 比如在这个例子中, 只有当你按下3次 Ctrl+C 时才会退出程序
        OnSignal: func(sig os.Signal, shutdown shutdownKeeper.ShutdownFunc) {
            signalCount++
            fmt.Printf("收到 SIGINT %d次\n", signalCount)
            if signalCount >= 3 {
                fmt.Println("关闭程序")
                shutdown()
            }
        },
    })

    keeper.Wait()
}
```

### 强制等待最大时间

```go
keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
    MaxHoldTime:       10 * time.Second,
    AlwaysHoldMaxTime: true, // 即使所有 token 都释放了,也要保证等待 10 秒后再退出
})
```

### 链式 HoldToken
链式HoldToken功能允许你创建具有依赖关系的 HoldToken 层级结构.子Token 会等待父Token 释放后才开始执行其关闭逻辑. 这种机制用于在一些复杂场景下确保多个有依赖关系模块的关闭操作的顺序性. 

```go
package main

import (
    "context"
    "fmt"
    "time"
    "os"
    "syscall"

    "github.com/hsldymq/shutdownKeeper/v2"
)

func main() {
    keeper := shutdownKeeper.NewKeeper(shutdownKeeper.KeeperOpts{
        Signals: []os.Signal{syscall.SIGINT, syscall.SIGTERM},
    })

    // 分别创建两个模块的 Token, 其中模块A的释放应该先于模块B
    moduleAToken := keeper.AllocHoldToken()
    moduleBToken := moduleAToken.AllocChainedToken()

    // 模块A的关闭逻辑
    moduleAToken.DoOnShutdown(func(ctx context.Context) {
        fmt.Printf("模块A开始关闭...")
        // 模拟一些清理工作
        time.Sleep(2 * time.Second)
        fmt.Println("模块A关闭完成")
    })

    // 模块B的关闭逻辑（会等待模块A关闭后才执行）
    moduleBToken.DoOnShutdown(func(ctx context.Context) {
        fmt.Printf("模块B开始关闭...")
        time.Sleep(1 * time.Second)
        fmt.Println("模块B关闭完成")
    })

    fmt.Println("应用程序启动, 按 Ctrl+C 测试有序关闭")
    keeper.Wait()
    fmt.Println("应用程序已按顺序优雅退出")
}
```

## 常见问题

### Q: 为什么不直接使用 context.WithCancel?

A: Context 只能传递取消信号,但不能确保所有 goroutine 都完成了清理工作.Shutdown Keeper 通过 HoldToken 机制确保每个子模块都有机会完成收尾工作.

### Q: 如果某个模块一直不释放 Token 怎么办?

A: 设置 `MaxHoldTime` 参数,超时后会强制退出.你也可以在每个模块内部设置自己的超时逻辑.

### Q: 可以在运行时动态分配 HoldToken 吗?

A: 可以！你可以随时调用 `AllocHoldToken()`,Keeper 会跟踪所有分配的 token.