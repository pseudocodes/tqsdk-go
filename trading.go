package tqsdk

import (
	"fmt"
	"strings"

	"github.com/gookit/goutil/dump"
	"go.uber.org/zap"
)

// AddAccount 添加期货账户
func (t *TQSDK) AddAccount(bid, userID, password string) (*TradeAccount, error) {
	// if t.brokers == nil {
	// 	return nil, fmt.Errorf("交易信息未初始化")
	// }

	if bid == "" || userID == "" || password == "" {
		return nil, fmt.Errorf("bid, userID, password 不能为空")
	}

	// // 检查期货公司是否支持
	// found := false
	// for _, broker := range t.brokers {
	// 	if broker == bid {
	// 		found = true
	// 		break
	// 	}
	// }
	// if !found {
	// 	return nil, fmt.Errorf("不支持该期货公司: %s", bid)
	// }

	key := t.getAccountKey(bid, userID)
	dump.V(key)

	t.tradeAccountsMu.Lock()
	defer t.tradeAccountsMu.Unlock()

	if account, exists := t.tradeAccounts[key]; exists {
		return account, nil
	}

	// 创建新的 DataManager
	initialData := map[string]interface{}{
		"trade": map[string]interface{}{
			userID: map[string]interface{}{
				"accounts": map[string]interface{}{
					"CNY": make(map[string]interface{}),
				},
				"trades":          make(map[string]interface{}),
				"positions":       make(map[string]interface{}),
				"orders":          make(map[string]interface{}),
				"his_settlements": make(map[string]interface{}),
			},
		},
	}
	dm := NewDataManager(initialData)

	// 注册数据更新回调
	dm.OnData(func() {
		t.Emit(EventRtnData, nil)
	})
	// dump.V(t.auth)
	// 获取交易服务器 URL
	brokerInfo, err := t.auth.GetTdUrl(bid, userID)
	if err != nil {
		return nil, fmt.Errorf("获取期货公司 URL 失败: %w", err)
	}

	urls := []string{
		brokerInfo.URL,
	}

	// 创建交易 WebSocket
	ws := NewTqTradeWebsocket(urls, dm, t.config.WsConfig)

	// 设置全局事件发射器，允许 WebSocket 层的事件穿透到 TQSDK 层
	ws.SetGlobalEmitter(t.EventEmitter)

	// 注册通知回调
	ws.OnNotify(func(notify NotifyEvent) {
		notify.BID = bid
		notify.UserID = userID
		t.Emit(EventNotify, notify)
	})

	// 初始化连接
	if err := ws.Init(false); err != nil {
		return nil, fmt.Errorf("初始化交易连接失败: %w", err)
	}

	account := &TradeAccount{
		BID:      bid,
		UserID:   userID,
		Password: password,
		Ws:       ws,
		Dm:       dm,
	}

	t.tradeAccounts[key] = account
	// dump.V(t.tradeAccounts)
	t.logger.Info("Added trade account",
		zap.String("bid", bid),
		zap.String("user_id", userID))

	return account, nil
}

// RemoveAccount 删除期货账户
func (t *TQSDK) RemoveAccount(bid, userID string) error {
	key := t.getAccountKey(bid, userID)

	t.tradeAccountsMu.Lock()
	defer t.tradeAccountsMu.Unlock()

	account, exists := t.tradeAccounts[key]
	if !exists {
		return fmt.Errorf("账户不存在: %s/%s", bid, userID)
	}

	if account.Ws != nil {
		account.Ws.Close()
	}

	delete(t.tradeAccounts, key)

	t.logger.Info("Removed trade account",
		zap.String("bid", bid),
		zap.String("user_id", userID))

	return nil
}

// Login 登录期货账户, 注意是期货账户名, 不是天勤账户名
func (t *TQSDK) Login(bid, userID, password string) error {

	if bid == "" || userID == "" || password == "" {
		return fmt.Errorf("bid, userID, password 不能为空")
	}
	if bid == "快期模拟" {
		userID = t.auth.AuthID
		password = t.auth.AuthID
	}

	account := t.getAccountRef(bid, userID)

	if account == nil {
		// 账户不存在，先添加
		t.logger.Debug("Account not found, adding account",
			zap.String("bid", bid),
			zap.String("user_id", userID))
		var err error
		account, err = t.AddAccount(bid, userID, password)
		if err != nil {
			t.logger.Error("Failed to add account",
				zap.String("bid", bid),
				zap.String("user_id", userID),
				zap.Error(err))
			return err
		}
	}

	loginContent := map[string]interface{}{
		"aid": "req_login",
	}

	if t.config.ClientAppID != "" {
		loginContent["client_app_id"] = t.config.ClientAppID
		loginContent["client_system_info"] = t.config.ClientSystemInfo
	}

	if account.Ws != nil {
		loginContent["bid"] = bid
		loginContent["user_name"] = userID
		loginContent["password"] = password
		account.Ws.Send(loginContent)

		t.logger.Info("Sent login request",
			zap.String("bid", bid),
			zap.String("user_id", userID))
	}

	return nil
}

// IsLogined 判断账户是否已登录
func (t *TQSDK) IsLogined(bid, userID string) bool {

	if bid == "快期模拟" {
		userID = t.auth.AuthID
	}

	account := t.getAccountRef(bid, userID)
	if account == nil || account.Dm == nil {
		return false
	}

	session := account.Dm.GetByPath([]string{"trade", userID, "session"})
	if session == nil {
		return false
	}
	sessionMap, ok := session.(map[string]interface{})
	if !ok {
		return false
	}

	tradingDay, hasTradingDay := sessionMap["trading_day"]
	if !hasTradingDay || tradingDay == nil || tradingDay == "" {
		return false
	}

	tradeMoreData := account.Dm.GetByPath([]string{"trade", userID, "trade_more_data"})
	dump.V(tradeMoreData)
	return tradeMoreData == false
}

// RefreshAccount 刷新账户信息
func (t *TQSDK) RefreshAccount(bid, userID string) error {
	account := t.getAccountRef(bid, userID)
	if account == nil || account.Ws == nil {
		return fmt.Errorf("账户不存在或未连接: %s/%s", bid, userID)
	}

	account.Ws.Send(map[string]string{"aid": "qry_account_info"})
	account.Ws.Send(map[string]string{"aid": "qry_account_register"})

	return nil
}

// RefreshAccounts 刷新所有账户信息
func (t *TQSDK) RefreshAccounts() {
	t.tradeAccountsMu.RLock()
	defer t.tradeAccountsMu.RUnlock()

	for _, account := range t.tradeAccounts {
		if account.Ws != nil {
			account.Ws.Send(map[string]string{"aid": "qry_account_info"})
			account.Ws.Send(map[string]string{"aid": "qry_account_register"})
		}
	}
}

// GetAllAccounts 获取所有账户信息
func (t *TQSDK) GetAllAccounts() []map[string]string {
	t.tradeAccountsMu.RLock()
	defer t.tradeAccountsMu.RUnlock()

	result := make([]map[string]string, 0, len(t.tradeAccounts))
	for _, account := range t.tradeAccounts {
		result = append(result, map[string]string{
			"bid":      account.BID,
			"user_id":  account.UserID,
			"password": account.Password,
		})
	}

	return result
}

// GetAccount 获取账户资金信息
func (t *TQSDK) GetAccount(bid, userID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"accounts", "CNY"})
}

// GetPosition 获取指定合约的持仓信息
func (t *TQSDK) GetPosition(bid, userID, symbol string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"positions", symbol})
}

// GetPositions 获取所有持仓信息
func (t *TQSDK) GetPositions(bid, userID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"positions"})
}

// GetOrder 获取指定委托单信息
func (t *TQSDK) GetOrder(bid, userID, orderID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"orders", orderID})
}

// GetOrders 获取所有委托单信息
func (t *TQSDK) GetOrders(bid, userID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"orders"})
}

// GetOrdersBySymbol 获取指定合约的所有委托单
func (t *TQSDK) GetOrdersBySymbol(bid, userID, symbol string) map[string]interface{} {
	orders := t.getAccountInfoByPaths(bid, userID, []string{"orders"})
	if orders == nil {
		return make(map[string]interface{})
	}

	parts := strings.Split(symbol, ".")
	if len(parts) != 2 {
		return make(map[string]interface{})
	}
	exchangeID, instrumentID := parts[0], parts[1]

	result := make(map[string]interface{})
	for orderID, orderData := range orders {
		if orderMap, ok := orderData.(map[string]interface{}); ok {
			if orderMap["exchange_id"] == exchangeID && orderMap["instrument_id"] == instrumentID {
				result[orderID] = orderData
			}
		}
	}

	return result
}

// GetTrade 获取指定成交记录
func (t *TQSDK) GetTrade(bid, userID, tradeID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"trades", tradeID})
}

// GetTrades 获取所有成交记录
func (t *TQSDK) GetTrades(bid, userID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"trades"})
}

// GetTradesByOrder 获取指定委托单的所有成交记录
func (t *TQSDK) GetTradesByOrder(bid, userID, orderID string) map[string]interface{} {
	trades := t.getAccountInfoByPaths(bid, userID, []string{"trades"})
	if trades == nil {
		return make(map[string]interface{})
	}

	result := make(map[string]interface{})
	for tradeID, tradeData := range trades {
		if tradeMap, ok := tradeData.(map[string]interface{}); ok {
			if tradeMap["order_id"] == orderID {
				result[tradeID] = tradeData
			}
		}
	}

	return result
}

// GetTradesBySymbol 获取指定合约的所有成交记录
func (t *TQSDK) GetTradesBySymbol(bid, userID, symbol string) map[string]interface{} {
	trades := t.getAccountInfoByPaths(bid, userID, []string{"trades"})
	if trades == nil {
		return make(map[string]interface{})
	}

	parts := strings.Split(symbol, ".")
	if len(parts) != 2 {
		return make(map[string]interface{})
	}
	exchangeID, instrumentID := parts[0], parts[1]

	result := make(map[string]interface{})
	for tradeID, tradeData := range trades {
		if tradeMap, ok := tradeData.(map[string]interface{}); ok {
			if tradeMap["exchange_id"] == exchangeID && tradeMap["instrument_id"] == instrumentID {
				result[tradeID] = tradeData
			}
		}
	}

	return result
}

// InsertOrder 下单
func (t *TQSDK) InsertOrder(bid, userID, exchangeID, instrumentID, direction, offset, priceType string, limitPrice float64, volume int64) (map[string]interface{}, error) {
	if !t.IsLogined(bid, userID) {
		return nil, fmt.Errorf("账户未登录: %s/%s", bid, userID)
	}

	account := t.getAccountRef(bid, userID)
	if account == nil {
		return nil, fmt.Errorf("账户不存在: %s/%s", bid, userID)
	}

	orderID := t.prefix + RandomStr(8)

	timeCondition := "GFD"
	if priceType == "ANY" {
		timeCondition = "IOC"
	}

	orderCommon := map[string]interface{}{
		"user_id":          userID,
		"order_id":         orderID,
		"exchange_id":      exchangeID,
		"instrument_id":    instrumentID,
		"direction":        direction,
		"offset":           offset,
		"price_type":       priceType,
		"limit_price":      limitPrice,
		"volume_condition": "ANY",
		"time_condition":   timeCondition,
	}

	orderInsert := map[string]interface{}{
		"aid":    "insert_order",
		"volume": volume,
	}
	for k, v := range orderCommon {
		orderInsert[k] = v
	}

	// 发送下单请求
	account.Ws.Send(orderInsert)

	// 初始化订单状态
	orderInit := map[string]interface{}{
		"volume_orign": volume,
		"status":       "ALIVE",
		"volume_left":  volume,
	}
	for k, v := range orderCommon {
		orderInit[k] = v
	}

	account.Dm.MergeData(map[string]interface{}{
		"trade": map[string]interface{}{
			userID: map[string]interface{}{
				"orders": map[string]interface{}{
					orderID: orderInit,
				},
			},
		},
	}, false, false)

	t.logger.Info("Inserted order",
		zap.String("order_id", orderID),
		zap.String("symbol", exchangeID+"."+instrumentID),
		zap.String("direction", direction),
		zap.String("offset", offset),
		zap.Float64("price", limitPrice),
		zap.Int64("volume", volume))

	order := account.Dm.GetByPath([]string{"trade", userID, "orders", orderID})
	if orderMap, ok := order.(map[string]interface{}); ok {
		return orderMap, nil
	}

	return make(map[string]interface{}), nil
}

// CancelOrder 撤销委托单
func (t *TQSDK) CancelOrder(bid, userID, orderID string) error {
	account := t.getAccountRef(bid, userID)
	if account == nil || account.Ws == nil {
		return fmt.Errorf("账户不存在或未连接: %s/%s", bid, userID)
	}

	account.Ws.Send(map[string]interface{}{
		"aid":      "cancel_order",
		"user_id":  userID,
		"order_id": orderID,
	})

	t.logger.Info("Cancelled order",
		zap.String("order_id", orderID),
		zap.String("user_id", userID))

	return nil
}

// ConfirmSettlement 确认结算单
func (t *TQSDK) ConfirmSettlement(bid, userID string) error {
	if bid == "快期模拟" {
		userID = t.auth.AuthID
	}
	account := t.getAccountRef(bid, userID)

	if account == nil || account.Ws == nil {
		return fmt.Errorf("账户不存在或未连接: %s/%s", bid, userID)
	}

	if account.hasConfirmed {
		return nil
	}

	account.hasConfirmed = true
	account.Ws.Send(map[string]string{"aid": "confirm_settlement"})

	t.logger.Info("Confirmed settlement", zap.String("user_id", userID))

	return nil
}

// GetHisSettlements 获取历史结算单列表
func (t *TQSDK) GetHisSettlements(bid, userID string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"his_settlements"})
}

// GetHisSettlement 获取指定日期的历史结算单
func (t *TQSDK) GetHisSettlement(bid, userID, tradingDay string) map[string]interface{} {
	return t.getAccountInfoByPaths(bid, userID, []string{"his_settlements", tradingDay})
}

// QueryHisSettlement 查询历史结算单
func (t *TQSDK) QueryHisSettlement(bid, userID, tradingDay string) error {
	account := t.getAccountRef(bid, userID)
	if account == nil || account.Ws == nil {
		return fmt.Errorf("账户不存在或未连接: %s/%s", bid, userID)
	}

	// 先检查 DataManager 中是否已有
	content := account.Dm.GetByPath([]string{"trade", userID, "his_settlements", tradingDay})
	if content != nil {
		return nil
	}

	// 发送查询请求
	account.Ws.Send(map[string]interface{}{
		"aid":         "qry_settlement_info",
		"trading_day": tradingDay,
	})

	return nil
}

// Transfer 银期转账
func (t *TQSDK) Transfer(bid, userID, bankID, bankPassword, futureAccount, futurePassword, currency string, amount float64) error {
	account := t.getAccountRef(bid, userID)
	if account == nil || account.Ws == nil {
		return fmt.Errorf("账户不存在或未连接: %s/%s", bid, userID)
	}

	if currency == "" {
		currency = "CNY"
	}

	account.Ws.Send(map[string]interface{}{
		"aid":             "req_transfer",
		"bank_id":         bankID,
		"bank_password":   bankPassword,
		"future_account":  futureAccount,
		"future_password": futurePassword,
		"currency":        currency,
		"amount":          amount,
	})

	t.logger.Info("Requested transfer",
		zap.String("user_id", userID),
		zap.Float64("amount", amount))

	return nil
}

// 辅助方法

// getAccountKey 获取账户唯一键
func (t *TQSDK) getAccountKey(bid, userID string) string {
	if bid != "" {
		return bid + "," + userID
	}

	// 如果只传了 userID，尝试查找唯一的账户
	t.tradeAccountsMu.RLock()
	defer t.tradeAccountsMu.RUnlock()

	foundBID := ""
	for key, account := range t.tradeAccounts {
		if account.UserID == userID {
			if foundBID != "" {
				// 找到多个相同 userID 的账户，无法确定
				return ""
			}
			foundBID = account.BID
		}
		_ = key
	}

	if foundBID != "" {
		return foundBID + "," + userID
	}

	return ""
}

// getAccountRef 获取账户引用
func (t *TQSDK) getAccountRef(bid, userID string) *TradeAccount {
	key := t.getAccountKey(bid, userID)
	if key == "" {
		return nil
	}

	t.tradeAccountsMu.RLock()
	defer t.tradeAccountsMu.RUnlock()
	t.logger.Debug("Getting account reference",
		zap.String("bid", bid),
		zap.String("user_id", userID),
		zap.String("key", key))
	return t.tradeAccounts[key]
}

// getAccountInfoByPaths 根据路径获取账户信息
func (t *TQSDK) getAccountInfoByPaths(bid, userID string, pathArray []string) map[string]interface{} {
	account := t.getAccountRef(bid, userID)
	if account == nil || account.Dm == nil {
		return nil
	}

	fullPath := append([]string{"trade", userID}, pathArray...)
	data := account.Dm.GetByPath(fullPath)

	if dataMap, ok := data.(map[string]interface{}); ok {
		return dataMap
	}

	return nil
}
