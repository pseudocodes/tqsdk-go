package syncapi

import "github.com/pseudocodes/tqsdk-go/shinny/v1alpha2/api"

// AccountRef is a live reference to account balance data.
// Counterpart: Python account = api.get_account()
type AccountRef struct {
	session api.TradeSession
}

func (r *AccountRef) refKey() refKey {
	return refKey{kind: refAccount, accountID: r.session.AccountID()}
}

// Get returns the latest account snapshot.
func (r *AccountRef) Get() api.Account {
	a, _ := r.session.Account()
	return a
}

// PositionRef is a live reference to a position.
// Counterpart: Python pos = api.get_position("SHFE.rb2501")
type PositionRef struct {
	symbol  string
	session api.TradeSession
}

func (r *PositionRef) refKey() refKey {
	return refKey{kind: refPosition, accountID: r.session.AccountID(), symbol: r.symbol}
}

// Get returns the latest position snapshot.
func (r *PositionRef) Get() api.Position {
	p, _ := r.session.Position(r.symbol)
	return p
}

// Symbol returns the instrument code.
func (r *PositionRef) Symbol() string { return r.symbol }

// OrderRef is a live reference to an order.
type SyncOrderRef struct {
	orderID string
	session api.TradeSession
}

func (r *SyncOrderRef) refKey() refKey {
	return refKey{kind: refOrder, orderID: r.orderID}
}

// Get returns the latest order snapshot.
func (r *SyncOrderRef) Get() api.Order {
	o, _ := r.session.Order(r.orderID)
	return o
}

// OrderID returns the order identifier.
func (r *SyncOrderRef) OrderID() string { return r.orderID }
