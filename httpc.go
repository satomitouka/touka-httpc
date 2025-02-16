package httpc

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 错误定义
var (
	ErrRequestTimeout     = errors.New("request timeout")
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	ErrBufferPoolEmpty    = errors.New("buffer pool exhausted")
)

// 缓冲池配置
const (
	bufferSize    = 32 << 10 // 32KB
	maxBufferPool = 100
)

// 默认User-Agent
const defaultUserAgent = "Touka HTTP Client"

var bufferPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, bufferSize))
	},
}

// DumpLogFunc 定义日志记录函数
// type DumpLogFunc func(ctx context.Context, method, url, transport string, headers http.Header)
type DumpLogFunc func(ctx context.Context, log string)

// Client 主客户端结构
type Client struct {
	client     *http.Client
	transport  *http.Transport
	retryOpts  RetryOptions
	bufferPool BufferPool
	userAgent  string
	dumpLog    DumpLogFunc // 日志记录函数
}

// RetryOptions 重试配置
type RetryOptions struct {
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	RetryStatuses []int
}

// BufferPool 缓冲池接口
type BufferPool interface {
	Get() *bytes.Buffer
	Put(*bytes.Buffer)
}

// 默认缓冲池实现
type defaultPool struct{}

func (p *defaultPool) Get() *bytes.Buffer {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func (p *defaultPool) Put(buf *bytes.Buffer) {
	if buf.Cap() > bufferSize*2 {
		return // 防止内存泄漏
	}
	bufferPool.Put(buf)
}

// New 创建客户端实例
func New(opts ...Option) *Client {
	// 智能MaxIdleConns 设置
	var maxIdleConns int = 0
	if runtime.GOMAXPROCS(0) > 4 {
		maxIdleConns = 128
	} else if runtime.GOMAXPROCS(0) != 1 {
		maxIdleConns = runtime.GOMAXPROCS(0) * 24
	} else {
		maxIdleConns = 32
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConns / 2,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	c := &Client{
		client: &http.Client{
			Transport: transport,
		},
		transport: transport,
		retryOpts: RetryOptions{
			MaxAttempts:   2,
			BaseDelay:     100 * time.Millisecond,
			MaxDelay:      1 * time.Second,
			RetryStatuses: []int{429, 500, 502, 503, 504},
		},
		bufferPool: &defaultPool{},
		userAgent:  defaultUserAgent,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// NewRequest 创建请求，支持与http.NewRequest兼容
func (c *Client) NewRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent) // 设置默认User-Agent
	return req, nil
}

// Option 配置选项类型
type Option func(*Client)

// WithTransport 自定义Transport，将非零字段合并到默认Transport中
func WithTransport(t *http.Transport) Option {
	return func(c *Client) {
		defaultTransport := c.transport
		mergeTransport(defaultTransport, t)
		// 更新Client的Transport
		c.transport = defaultTransport
		c.client.Transport = defaultTransport
	}
}

// mergeTransport 将src的非零字段合并到dst中
func mergeTransport(dst, src *http.Transport) {
	dstVal := reflect.ValueOf(dst).Elem()
	srcVal := reflect.ValueOf(src).Elem()

	for i := 0; i < srcVal.NumField(); i++ {
		srcField := srcVal.Field(i)
		srcType := srcVal.Type().Field(i)
		// 跳过不可导出字段
		if srcType.PkgPath != "" {
			continue
		}
		dstField := dstVal.FieldByName(srcType.Name)
		if !dstField.IsValid() || !dstField.CanSet() {
			continue
		}
		// 检查src字段是否为零值，非零则复制
		if !isZero(srcField) {
			dstField.Set(srcField)
		}
	}
}

// isZero 检查反射值是否为对应类型的零值
func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		z := reflect.Zero(v.Type())
		return v.Interface() == z.Interface()
	}
}

// WithBufferPool 自定义缓冲池
func WithBufferPool(pool BufferPool) Option {
	return func(c *Client) {
		c.bufferPool = pool
	}
}

// WithRetryOptions 自定义重试策略
func WithRetryOptions(opts RetryOptions) Option {
	return func(c *Client) {
		c.retryOpts = opts
	}
}

// WithUserAgent 设置自定义User-Agent
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		c.userAgent = ua
	}
}

// WithDumpLog 启用默认日志记录功能
func WithDumpLog() Option {
	return func(c *Client) {
		// 使用默认的日志记录函数
		c.dumpLog = func(ctx context.Context, log string) {
			fmt.Println(log) // 默认打印到标准输出
		}
	}
}

// WithDumpLogFunc 自定义日志记录功能
func WithDumpLogFunc(dumpLog DumpLogFunc) Option {
	return func(c *Client) {
		c.dumpLog = dumpLog
	}
}

// 实现标准库兼容接口
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.ProtoMajor == 2 {
		if req.Header.Get("Connection") == "Upgrade" && req.Header.Get("Upgrade") != "" {
			req.Header.Del("Connection")
			req.Header.Del("Upgrade")
		}
	}

	// 记录日志
	c.logRequest(req)

	return c.doWithRetry(req)
}

// 记录请求日志
func (c *Client) logRequest(req *http.Request) {
	if c.dumpLog == nil {
		return
	}

	// 提取 Transport 详细信息
	transportDetails := getTransportDetails(c.transport)

	// 构造美观的日志内容
	logContent := fmt.Sprintf(`
[HTTP Request Log]
-------------------------------
Time       : %s
Method     : %s
URL        : %s
Host       : %s
Protocol   : %s
Transport  : 
%v
Headers    :
%v
-------------------------------
`,
		time.Now().Format("2006-01-02 15:04:05"), // 当前时间
		req.Method,                               // 请求方法
		req.URL.String(),                         // 请求完整 URL
		req.URL.Host,                             // 请求主机
		req.Proto,                                // 请求协议版本
		transportDetails,                         // Transport 详细信息
		formatHeaders(req.Header),                // 格式化后的请求头
	)

	// 调用日志记录函数
	c.dumpLog(req.Context(), logContent)
}

// 获取 Transport 的详细信息
func getTransportDetails(transport http.RoundTripper) string {
	// 检查是否为标准的 *http.Transport 类型
	if t, ok := transport.(*http.Transport); ok {
		return fmt.Sprintf(`  Type                 : *http.Transport
  MaxIdleConns         : %d
  MaxIdleConnsPerHost  : %d
  MaxConnsPerHost      : %d
  IdleConnTimeout      : %s
  TLSHandshakeTimeout  : %s
  DisableKeepAlives    : %v
  WriteBufferSize      : %d
  ReadBufferSize       : %d
`,
			t.MaxIdleConns,
			t.MaxIdleConnsPerHost,
			t.MaxConnsPerHost,
			t.IdleConnTimeout,
			t.TLSHandshakeTimeout,
			t.DisableKeepAlives,
			t.WriteBufferSize,
			t.ReadBufferSize,
		)
	}

	// 如果是其他类型的 Transport，返回类型名称
	if transport != nil {
		return fmt.Sprintf("  Type                 : %T", transport)
	}

	// 如果 Transport 为空
	return "  Type                 : nil"
}

// 格式化请求头为多行字符串
func formatHeaders(headers http.Header) string {
	var builder strings.Builder
	for key, values := range headers {
		builder.WriteString(fmt.Sprintf("  %s: %s\n", key, strings.Join(values, ", ")))
	}
	return builder.String()
}

// 高性能BufferCopy实现
func (c *Client) bufferCopy(dst io.Writer, src io.Reader) (written int64, err error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	for {
		nr, er := src.Read(buf.Bytes()[:bufferSize])
		if nr > 0 {
			nw, ew := dst.Write(buf.Bytes()[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return
}

func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; attempt <= c.retryOpts.MaxAttempts; attempt++ {
		resp, err = c.client.Do(req)

		// 检查是否需要重试
		if c.shouldRetry(resp, err) {
			if attempt < c.retryOpts.MaxAttempts {
				// 仅在 429 时解析 Retry-After，其他情况使用指数退避
				var delay time.Duration
				if resp != nil && resp.StatusCode == 429 {
					delay = c.calculateRetryAfter(resp)
				} else {
					delay = c.calculateExponentialBackoff(attempt)
				}
				time.Sleep(delay)
				continue
			}
			return nil, ErrMaxRetriesExceeded
		}
		break
	}

	if err != nil {
		return nil, c.wrapError(err)
	}

	return resp, nil
}

// 解析 Retry-After 头部，仅在状态码为 429 时调用
func (c *Client) calculateRetryAfter(resp *http.Response) time.Duration {
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter != "" {
		// 尝试解析 Retry-After
		if delay, err := parseRetryAfter(retryAfter); err == nil {
			return delay
		}
	}

	// 如果 Retry-After 不存在或无效，返回默认的最小延迟时间
	return c.retryOpts.BaseDelay
}

// 解析 Retry-After 的具体实现
func parseRetryAfter(retryAfter string) (time.Duration, error) {
	// 尝试解析为秒数
	if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil {
		return seconds, nil
	}

	// 尝试解析为 HTTP 日期
	if retryTime, err := http.ParseTime(retryAfter); err == nil {
		delay := time.Until(retryTime)
		if delay > 0 {
			return delay, nil
		}
	}

	return 0, errors.New("invalid Retry-After value")
}

// 指数退避计算
func (c *Client) calculateExponentialBackoff(attempt int) time.Duration {
	delay := c.retryOpts.BaseDelay * time.Duration(1<<uint(attempt))
	if delay > c.retryOpts.MaxDelay {
		return c.retryOpts.MaxDelay
	}
	return delay
}

// 错误包装
func (c *Client) wrapError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %v", ErrRequestTimeout, err)
	default:
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return fmt.Errorf("%w: %v", ErrRequestTimeout, err)
		}
		return err
	}
}

// 重试条件判断
func (c *Client) shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return isNetworkError(err)
	}

	// 检查是否需要重试的状态码
	for _, status := range c.retryOpts.RetryStatuses {
		if resp.StatusCode == status {
			return true
		}
	}
	return false
}

/*
// 指数退避计算
func (c *Client) calculateDelay(attempt int) time.Duration {
	delay := c.retryOpts.BaseDelay * time.Duration(1<<uint(attempt))
	if delay > c.retryOpts.MaxDelay {
		return c.retryOpts.MaxDelay
	}
	return delay
}
*/

// JSON响应处理（使用缓冲池）
func (c *Client) DecodeJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf := c.bufferPool.Get()
		defer c.bufferPool.Put(buf)

		_, err := c.bufferCopy(buf, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read error body: %w", err)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, buf.String())
	}

	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("%v", err)
	}

	return json.Unmarshal(buf.Bytes(), v)
}

// XML响应处理（使用缓冲池）
func (c *Client) DecodeXML(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf := c.bufferPool.Get()
		defer c.bufferPool.Put(buf)

		_, err := c.bufferCopy(buf, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read error body: %w", err)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, buf.String())
	}

	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("%v", err)
	}

	return xml.Unmarshal(buf.Bytes(), v)
}

// 辅助函数
func isNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

// 标准库兼容方法
func (c *Client) Get(url string) (*http.Response, error) {
	return c.GetContext(context.Background(), url)
}

func (c *Client) GetContext(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// 高级POST方法
func (c *Client) PostJSON(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	return c.Do(req)
}

// 高级POST方法支持XML
func (c *Client) PostXML(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	if err := xml.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("User-Agent", c.userAgent)

	return c.Do(req)
}

// 高级PUT方法
func (c *Client) PutJSON(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	return c.Do(req)
}

// 高级DELETE方法
func (c *Client) Delete(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)

	return c.Do(req)
}
