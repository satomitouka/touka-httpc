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
	"net/http/httptrace"
	"runtime"
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

// Client 主客户端结构
type Client struct {
	client      *http.Client
	transport   *http.Transport
	retryOpts   RetryOptions
	bufferPool  BufferPool
	userAgent   string
	dumpEnabled bool
}

// RetryOptions 重试配置
type RetryOptions struct {
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	RetryStatuses []int
}

type traceInfo struct {
	dnsStart     httptrace.DNSStartInfo
	dnsDone      httptrace.DNSDoneInfo
	connectStart map[string]string // network -> addr
	connectDone  map[string]error  // "network addr" -> error
	gotConn      httptrace.GotConnInfo
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
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   runtime.GOMAXPROCS(0) * 2,
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

// WithTransport 自定义Transport
func WithTransport(t *http.Transport) Option {
	return func(c *Client) {
		c.transport = t
		c.client.Transport = t
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

// 实现标准库兼容接口
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.ProtoMajor == 2 {
		if req.Header.Get("Connection") == "Upgrade" && req.Header.Get("Upgrade") != "" {
			req.Header.Del("Connection")
			req.Header.Del("Upgrade")
		}
	}

	// 保存原始请求体
	var originalBody []byte
	var err error
	if req.Body != nil {
		originalBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(originalBody))
	}

	return c.doWithRetry(req, originalBody)
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

// 带指数退避的重试逻辑
func (c *Client) doWithRetry(originalReq *http.Request, originalBody []byte) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; attempt <= c.retryOpts.MaxAttempts; attempt++ {
		req := originalReq.Clone(originalReq.Context())
		req.Body = io.NopCloser(bytes.NewReader(originalBody))

		var traceData *traceInfo
		if c.dumpEnabled {
			traceData = &traceInfo{}
			trace := createTrace(traceData)
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
		}

		// 记录请求日志
		if c.dumpEnabled {
			c.dumpRequest(req, originalBody)
		}

		resp, err = c.client.Do(req)

		// 记录响应日志
		if c.dumpEnabled {
			var respBody []byte
			if resp != nil {
				respBody, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))
			}
			c.dumpResponse(resp, err, traceData)
		}

		if c.shouldRetry(resp, err) {
			if attempt < c.retryOpts.MaxAttempts {
				delay := c.calculateDelay(attempt)
				time.Sleep(delay)
				continue
			}
			return nil, ErrMaxRetriesExceeded
		}
		break
	}

	return resp, c.wrapError(err)
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

	for _, status := range c.retryOpts.RetryStatuses {
		if resp.StatusCode == status {
			return true
		}
	}
	return false
}

// 指数退避计算
func (c *Client) calculateDelay(attempt int) time.Duration {
	delay := c.retryOpts.BaseDelay * time.Duration(1<<uint(attempt))
	if delay > c.retryOpts.MaxDelay {
		return c.retryOpts.MaxDelay
	}
	return delay
}

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
	req.Header.Set("User-Agent", c.userAgent) // 设置默认User-Agent

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
	req.Header.Set("User-Agent", c.userAgent) // 设置默认User-Agent

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
	req.Header.Set("User-Agent", c.userAgent) // 设置默认User-Agent

	return c.Do(req)
}

// 高级DELETE方法
func (c *Client) Delete(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent) // 设置默认User-Agent

	return c.Do(req)
}

// WithDumpLog 启用dump日志的选项
func WithDumpLog() Option {
	return func(c *Client) {
		c.dumpEnabled = true
	}
}

func createTrace(data *traceInfo) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart: func(info httptrace.DNSStartInfo) {
			data.dnsStart = info
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			data.dnsDone = info
		},
		ConnectStart: func(network, addr string) {
			if data.connectStart == nil {
				data.connectStart = make(map[string]string)
			}
			data.connectStart[network] = addr
		},
		ConnectDone: func(network, addr string, err error) {
			if data.connectDone == nil {
				data.connectDone = make(map[string]error)
			}
			data.connectDone[network+" "+addr] = err
		},
		GotConn: func(info httptrace.GotConnInfo) {
			data.gotConn = info
		},
	}
}

func (c *Client) dumpRequest(req *http.Request, body []byte) {
	fmt.Println("=== HTTP Request Dump ===")
	fmt.Printf("Method: %s\n", req.Method)
	fmt.Printf("URL: %s\n", req.URL.String())
	fmt.Printf("Proto: %s\n", req.Proto)
	fmt.Println("Headers:")
	for k, v := range req.Header {
		fmt.Printf("  %s: %v\n", k, v)
	}
	fmt.Printf("Body: %s\n", string(body))
	fmt.Println("Transport Parameters:")
	fmt.Printf("  MaxIdleConns: %d\n", c.transport.MaxIdleConns)
	fmt.Printf("  MaxIdleConnsPerHost: %d\n", c.transport.MaxIdleConnsPerHost)
	fmt.Printf("  IdleConnTimeout: %s\n", c.transport.IdleConnTimeout)
	fmt.Printf("  TLSHandshakeTimeout: %s\n", c.transport.TLSHandshakeTimeout)
	fmt.Println("-------------------------")
}

func (c *Client) dumpResponse(resp *http.Response, err error, traceData *traceInfo) {
	fmt.Println("=== HTTP Response Dump ===")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	if resp == nil {
		fmt.Println("Response is nil")
		return
	}
	fmt.Printf("Status: %s\n", resp.Status)
	fmt.Printf("StatusCode: %d\n", resp.StatusCode)
	fmt.Println("Headers:")
	for k, v := range resp.Header {
		fmt.Printf("  %s: %v\n", k, v)
	}
	if resp.Body != nil {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Body: %s\n", string(body))
	}

	if traceData != nil {
		fmt.Println("Trace Info:")
		fmt.Printf("DNS Lookup: %s → %+v\n", traceData.dnsStart.Host, traceData.dnsDone.Addrs)
		fmt.Println("Connection Events:")
		for network, addr := range traceData.connectStart {
			fmt.Printf("  Started %s connection to %s\n", network, addr)
		}
		for key, err := range traceData.connectDone {
			fmt.Printf("  Completed connection to %s (error: %v)\n", key, err)
		}
		fmt.Printf("Connection Reused: %v\n", traceData.gotConn.Reused)
	}
	fmt.Println("=========================")
}
