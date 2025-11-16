package tqsdk

import "sync"

// events_callback.go - 方案 2：基于回调函数的事件系统
// 这个文件展示了使用回调函数实现事件系统的方案
// 优点：类似 JavaScript API、支持多订阅者、灵活的订阅/取消订阅
// 缺点：需要自己实现、线程安全需要额外处理

// EventType 事件类型
type EventType string

const (
	EventReady      EventType = "ready"       // 收到合约基础数据
	EventRtnData    EventType = "rtn_data"    // 数据更新
	EventRtnBrokers EventType = "rtn_brokers" // 收到期货公司列表
	EventNotify     EventType = "notify"      // 收到通知
	EventError      EventType = "error"       // 错误事件
)

// EventHandler 事件处理器函数
type EventHandler func(data interface{})

// EventEmitter 事件发射器
type EventEmitter struct {
	handlers map[EventType][]EventHandler
	mu       sync.RWMutex
}

// NewEventEmitter 创建新的事件发射器
func NewEventEmitter() *EventEmitter {
	return &EventEmitter{
		handlers: make(map[EventType][]EventHandler),
	}
}

// On 注册事件处理器
func (e *EventEmitter) On(event EventType, handler EventHandler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.handlers[event] == nil {
		e.handlers[event] = make([]EventHandler, 0)
	}
	e.handlers[event] = append(e.handlers[event], handler)
}

// Off 移除事件处理器
// 注意：这个实现通过函数指针比较来移除，Go 中函数比较有限制
// 实际使用时可能需要返回一个 token 或 ID 来标识处理器
func (e *EventEmitter) Off(event EventType, handler EventHandler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	handlers := e.handlers[event]
	if handlers == nil {
		return
	}

	// 注意：Go 中函数比较有限制，这里只是示例
	// 实际使用时建议使用 ID 或 token 机制
	newHandlers := make([]EventHandler, 0)
	for _, h := range handlers {
		// 这个比较在 Go 中可能不工作，仅作示例
		// if &h != &handler {
		// 	newHandlers = append(newHandlers, h)
		// }
		newHandlers = append(newHandlers, h)
	}
	e.handlers[event] = newHandlers
}

// Once 注册一次性事件处理器
func (e *EventEmitter) Once(event EventType, handler EventHandler) {
	var wrapper EventHandler
	wrapper = func(data interface{}) {
		handler(data)
		e.Off(event, wrapper)
	}
	e.On(event, wrapper)
}

// Emit 触发事件
func (e *EventEmitter) Emit(event EventType, data interface{}) {
	e.mu.RLock()
	handlers := e.handlers[event]
	e.mu.RUnlock()

	if handlers == nil {
		return
	}

	// 异步调用所有处理器
	for _, handler := range handlers {
		go handler(data)
	}
}

// EmitSync 同步触发事件
func (e *EventEmitter) EmitSync(event EventType, data interface{}) {
	e.mu.RLock()
	handlers := e.handlers[event]
	e.mu.RUnlock()

	if handlers == nil {
		return
	}

	// 同步调用所有处理器
	for _, handler := range handlers {
		handler(data)
	}
}

// RemoveAllListeners 移除指定事件的所有监听器
func (e *EventEmitter) RemoveAllListeners(event EventType) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.handlers, event)
}

// ListenerCount 获取指定事件的监听器数量
func (e *EventEmitter) ListenerCount(event EventType) int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.handlers[event])
}
