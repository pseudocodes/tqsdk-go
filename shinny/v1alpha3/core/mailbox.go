package core

import "sync"

// DefaultMailboxSize 是订阅邮箱的默认容量。
const DefaultMailboxSize = 1024

// mailbox 是一个订阅的有界事件队列。满时不等待消费者：终止订阅，
// 已入队的连续前缀仍可读出，随后 channel 关闭，Err 返回 ErrSlowConsumer。
type mailbox[E any] struct {
	mu   sync.Mutex
	ch   chan E
	done bool
	err  error
}

func newMailbox[E any](size int) *mailbox[E] {
	if size <= 0 {
		size = DefaultMailboxSize
	}
	return &mailbox[E]{ch: make(chan E, size)}
}

// push 非阻塞投递；订阅已终止返回 false。
func (m *mailbox[E]) push(e E) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return false
	}
	select {
	case m.ch <- e:
		return true
	default:
		m.done = true
		m.err = ErrSlowConsumer
		close(m.ch)
		return false
	}
}

// close 以 err 结束订阅；正常关闭传 nil。幂等。
func (m *mailbox[E]) close(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return
	}
	m.done = true
	m.err = err
	close(m.ch)
}

func (m *mailbox[E]) closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done
}

func (m *mailbox[E]) error() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}
