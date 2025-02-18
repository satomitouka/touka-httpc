# Touka-HTTPC

## 项目简介

`touka-httpc` 是一个用 Go 语言编写的高性能 HTTP 客户端库，旨在提供一个简洁、易用且功能强大的 HTTP 请求工具。它在 `net/http` 标准库的基础上进行了封装和增强，提供了更便捷的 API、更灵活的配置选项以及更完善的重试和中间件机制，以满足各种复杂的 HTTP 客户端需求。

## 特性

*   **简洁易用的 API:**  提供链式调用的 `RequestBuilder` 构建请求，简化 HTTP 请求的创建和发送过程。
*   **灵活的配置选项:**  支持自定义 Transport、连接池参数、超时时间、重试策略、缓冲池大小等，满足各种性能和功能需求。
*   **强大的重试机制:**  内置可配置的重试策略，包括最大重试次数、退避算法、Jitter 抖动等，增强客户端的稳定性和可靠性。
*   **中间件支持:**  提供中间件机制，方便用户自定义请求和响应处理逻辑，例如日志记录、认证、链路追踪等。
*   **高性能缓冲池:**  内置缓冲池，复用 `bytes.Buffer`，减少内存分配，提升性能。
*   **HTTP/2 Cleartext (H2C) 支持:**  可选启用 H2C 支持，提升在支持 HTTP/2 服务端的性能。
*   **全面的响应处理:**  支持 JSON、XML、Text、Bytes 等多种响应格式的解码，并提供便捷的错误处理机制。
*   **标准库兼容:**  API 设计兼容 `net/http` 标准库，方便用户迁移和使用。
*   **详细的请求日志:**  可选启用请求日志记录，方便调试和问题排查。

## 安装

使用 `go get` 命令安装 `touka-httpc`：

```bash
go get https://github.com/satomitouka/touka-httpc
```

## 快速开始

以下是一个简单的 GET 请求示例：

```go
package main

import (
	"context"
	"fmt"
	httpc "github.com/satomitouka/touka-httpc"
)

func main() {
	client := httpc.New()

	resp, err := client.GET("https://httpbin.org/get").Execute()
	if err != nil {
		fmt.Println("请求失败:", err)
		return
	}
	defer resp.Body.Close()

	text, err := client.DecodeTextResponse(resp)
	if err != nil {
		fmt.Println("解析响应失败:", err)
		return
	}

	fmt.Println("响应内容:", text)
}
```

## 详细用法

### 创建客户端

使用 `httpc.New()` 函数创建 `Client` 实例。可以通过 `Option` 函数自定义客户端配置。

```go
client := httpc.New(
    httpc.WithTimeout(5 * time.Second),                 // 设置默认请求超时时间为 5 秒
    httpc.WithMaxIdleConns(200),                       // 设置最大空闲连接数
    httpc.WithDumpLog(),                               // 启用默认日志记录
    httpc.WithRetryOptions(httpc.RetryOptions{         // 自定义重试策略
        MaxAttempts:   3,
        BaseDelay:     200 * time.Millisecond,
        MaxDelay:      2 * time.Second,
        RetryStatuses: []int{429, 503},
        Jitter:        true,
    }),
)
```

**可用配置选项 (Option):**

*   `WithTransport(t *http.Transport)`:  自定义 `http.Transport`，用于更底层的连接控制。
*   `WithMaxIdleConns(maxIdleConns int)`: 设置连接池中最大空闲连接数。
*   `WithIdleConnTimeout(idleConnTimeout time.Duration)`: 设置空闲连接的超时时间。
*   `WithDialTimeout(dialTimeout time.Duration)`: 设置建立连接的超时时间。
*   `WithKeepAliveTimeout(keepAliveTimeout time.Duration)`: 设置 Keep-Alive 连接的超时时间。
*   `WithTLSHandshakeTimeout(tlsHandshakeTimeout time.Duration)`: 设置 TLS 握手超时时间。
*   `WithExpectContinueTimeout(expectContinueTimeout time.Duration)`: 设置等待服务端返回 "100 Continue" 响应的超时时间。
*   `WithBufferSize(bufferSize int)`: 自定义缓冲池中 `bytes.Buffer` 的初始大小。
*   `WithMaxBufferPoolSize(maxBufferPool int)`: 自定义缓冲池的最大容量。
*   `WithTimeout(timeout time.Duration)`: 设置客户端的默认请求超时时间。
*   `WithBufferPool(pool httpc.BufferPool)`:  使用自定义缓冲池实现。
*   `WithRetryOptions(opts httpc.RetryOptions)`:  自定义重试策略。
*   `WithUserAgent(ua string)`:  设置自定义 User-Agent 头部。
*   `WithDumpLog()`: 启用默认的请求日志记录 (输出到控制台)。
*   `WithDumpLogFunc(dumpLog httpc.DumpLogFunc)`:  使用自定义的日志记录函数。
*   `WithMiddleware(middleware ...httpc.MiddlewareFunc)`:  添加中间件函数。
*   `WithH2C()`: 启用 HTTP/2 Cleartext (H2C) 支持。

### 请求构建器 (RequestBuilder)

`Client` 提供了 `NewRequestBuilder(method, urlStr string)` 方法来创建一个 `RequestBuilder` 实例。`RequestBuilder` 采用链式调用，允许你逐步构建 HTTP 请求的各个部分，使请求创建过程更加清晰和简洁。

```go
builder := client.NewRequestBuilder(http.MethodGet, "https://httpbin.org/get")
```

**RequestBuilder 方法详解:**

*   `WithContext(ctx context.Context)`: 设置请求的 Context，用于控制请求的生命周期，例如超时、取消等。

    ```go
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    resp, err := client.GET("https://httpbin.org/delay/2").WithContext(ctx).Execute()
    // ...
    ```

*   `SetHeader(key, value string)`: 设置请求头，会**覆盖**已存在的同名 Header。

    ```go
    builder := client.POST("https://httpbin.org/post").SetHeader("Content-Type", "application/json")
    ```

*   `AddHeader(key, value string)`: 添加请求头，**不会覆盖**已存在的同名 Header，而是追加值。

    ```go
    builder := client.GET("https://httpbin.org/get").AddHeader("Accept-Encoding", "gzip").AddHeader("Accept-Encoding", "deflate")
    ```

*   `SetHeaders(headers map[string]string)`: 批量设置请求头。

    ```go
    builder := client.PUT("https://httpbin.org/put").SetHeaders(map[string]string{
        "X-Request-ID": "request-123",
        "Authorization":  "Bearer token...",
    })
    ```

*   `SetQueryParam(key, value string)`: 设置 URL Query 参数，会**覆盖**已存在的同名参数。

    ```go
    builder := client.GET("https://httpbin.org/get)").SetQueryParam("param1", "value1")
    ```

*   `AddQueryParam(key, value string)`: 添加 URL Query 参数，**不会覆盖**已存在的同名参数，而是追加值。

    ```go
    builder := client.GET("https://httpbin.org/get").AddQueryParam("param1", "value1").AddQueryParam("param1", "value2")
    ```

*   `SetQueryParams(params map[string]string)`: 批量设置 URL Query 参数。

    ```go
    builder := client.GET("https://httpbin.org/get").SetQueryParams(map[string]string{
        "param1": "value1",
        "param2": "value2",
    })
    ```

*   `SetBody(body io.Reader)`: 设置请求 Body，接受 `io.Reader` 接口，用于自定义 Body 内容。

    ```go
    body := bytes.NewBufferString(`plain text body`)
    builder := client.POST("https://httpbin.org/post").SetBody(body)
    ```

*   `SetJSONBody(body interface{}) (*RequestBuilder, error)`: 设置 JSON 格式的请求 Body，自动设置 `Content-Type: application/json`。

    ```go
    data := map[string]string{"key": "value"}
    builder, err := client.POST("https://httpbin.org/post").SetJSONBody(data)
    ```

*   `SetXMLBody(body interface{}) (*RequestBuilder, error)`: 设置 XML 格式的请求 Body，自动设置 `Content-Type: application/xml`。

    ```go
    data := struct {
        XMLName xml.Name `xml:"data"`
        Key     string   `xml:"key"`
    }{Key: "value"}
    builder, err := client.PUT("https://httpbin.org/put").SetXMLBody(data)
    ```

*   `Build() (*http.Request, error)`: 构建 `http.Request` 对象，可以手动执行请求或进行其他处理。
*   `Execute() (*http.Response, error)`: 执行请求并返回 `http.Response` 对象。这是最常用的方法，用于发送请求并获取原始响应。
*   `DecodeJSON(v interface{}) error`: 执行请求并解析 JSON 响应到 `v` 变量，方便直接获取结构化数据。
*   `DecodeXML(v interface{}) error`: 执行请求并解析 XML 响应到 `v` 变量，用于处理 XML 格式的响应。
*   `Text() (string, error)`: 执行请求并获取 Text 格式的响应 Body，适用于文本响应。
*   `Bytes() ([]byte, error)`: 执行请求并获取 Bytes 格式的响应 Body，适用于二进制数据或需要自行处理的响应。

**链式调用:**

`RequestBuilder` 的方法支持链式调用，允许你以更简洁的方式构建复杂的请求：

```go
resp, err := client.GET("https://httpbin.org/get").
    SetHeader("X-Custom-Header", "custom-value").
    SetQueryParam("param1", "value1").
    Execute()
```

**错误处理:**

`RequestBuilder` 的 `Execute`, `DecodeJSON`, `DecodeXML`, `Text`, `Bytes` 等方法会自动处理 HTTP 错误状态码 (>= 400)。当响应状态码表示错误时，这些方法会返回 error，error 信息中包含了状态码和响应 Body (错误信息)。

### 请求方法 (Request Methods)

`Client` 提供了快捷方法用于创建不同 HTTP 方法的 `RequestBuilder`：

*   `GET(urlStr string) *RequestBuilder`
*   `POST(urlStr string) *RequestBuilder`
*   `PUT(urlStr string) *RequestBuilder`
*   `DELETE(urlStr string) *RequestBuilder`
*   `PATCH(urlStr string) *RequestBuilder`
*   `HEAD(urlStr string) *RequestBuilder`
*   `OPTIONS(urlStr string) *RequestBuilder`

### 响应处理

`RequestBuilder` 提供了多种方法用于处理不同格式的响应：

*   `DecodeJSON(v interface{}) error`: 将 JSON 响应解码到指定的结构体 `v` 中。
*   `DecodeXML(v interface{}) error`: 将 XML 响应解码到指定的结构体 `v` 中。
*   `Text() (string, error)`:  将响应 Body 读取为字符串。
*   `Bytes() ([]byte, error)`: 将响应 Body 读取为字节切片。

这些方法会自动处理错误状态码 (>= 400)，并返回包含状态码和错误 Body 的错误信息。

### 超时设置

可以通过以下方式设置超时时间：

*   **全局超时:**  在创建 `Client` 时，使用 `WithTimeout(timeout time.Duration)` 设置默认的请求超时时间。
*   **请求级别超时:**  使用 `RequestBuilder.WithContext(ctx context.Context)` 方法，通过 Context 的 `Timeout` 或 `Deadline` 控制单个请求的超时时间。

请求级别的超时设置优先级高于全局超时设置。

### 重试机制

`touka-httpc` 内置了可配置的重试机制，可以通过 `WithRetryOptions`  选项进行自定义。

**RetryOptions 配置项:**

*   `MaxAttempts`: 最大重试次数。
*   `BaseDelay`: 初始重试延迟。
*   `MaxDelay`: 最大重试延迟。
*   `RetryStatuses`: 需要重试的 HTTP 状态码列表。
*   `Jitter`: 是否启用 Jitter 抖动，避免 "惊群效应"。

默认重试策略为：最大重试 2 次，初始延迟 100ms，最大延迟 1s，重试状态码为 429, 500, 502, 503, 504，默认不启用 Jitter。

### 中间件

`touka-httpc` 支持中间件机制，允许用户在请求发送前后以及响应处理前后插入自定义逻辑。

**中间件函数类型:**

```go
type MiddlewareFunc func(next http.Handler) http.Handler
```

中间件函数接收一个 `http.Handler` 作为参数 (代表下一个中间件或最终的请求处理器)，并返回一个新的 `http.Handler`。

**示例中间件 (日志记录):**

```go
func LoggingMiddleware() httpc.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startTime := time.Now()
			next.ServeHTTP(w, r) // 调用下一个 Handler
			duration := time.Since(startTime)
			fmt.Printf("[%s] %s %s %s\n", time.Now().Format("2006-01-02 15:04:05"), r.Method, r.URL.String(), duration)
		})
	}
}

// 创建客户端并添加中间件
client := httpc.New(
    httpc.WithMiddleware(LoggingMiddleware()),
)
```

### 日志记录

`touka-httpc` 提供了请求日志记录功能，可以记录详细的请求信息，方便调试和问题排查。

*   **默认日志记录:**  使用 `WithDumpLog()` 选项启用默认日志记录，日志将输出到控制台。
*   **自定义日志记录:**  使用 `WithDumpLogFunc(dumpLog httpc.DumpLogFunc)` 选项，传入自定义的日志记录函数。

### H2C 支持

通过 `WithH2C()` 选项可以启用 HTTP/2 Cleartext (H2C) 支持。启用后，`touka-httpc` 将尝试与服务端建立 HTTP/2 明文连接 (如果服务端支持)。

```go
client := httpc.New(
    httpc.WithH2C(), // 启用 H2C 支持
)
```

### 缓冲池

`touka-httpc` 内置了缓冲池，用于复用 `bytes.Buffer`，减少内存分配，提升性能。可以通过 `WithBufferSize` 和 `WithMaxBufferPoolSize` 选项自定义缓冲池的配置。

## 错误定义

`touka-httpc` 定义了以下错误类型：

*   `ErrRequestTimeout`: 请求超时错误。
*   `ErrMaxRetriesExceeded`: 超过最大重试次数错误。
*   `ErrDecodeResponse`: 响应 Body 解码失败错误。
*   `ErrInvalidURL`: URL 无效错误。

## 贡献

欢迎提交 issue 和 pull request，共同完善 `touka-httpc`。

## 许可证

WJQserver Studio License 2.0 (WJQserver Studio 许可证 2.0)