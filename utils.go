package tqsdk

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// RandomStr 生成指定长度的随机字符串
func RandomStr(length int) string {
	const charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	
	result := make([]byte, length)
	for i := range result {
		result[i] = charset[r.Intn(len(charset))]
	}
	return string(result)
}

// IsEmptyObject 检查对象是否为空
func IsEmptyObject(obj interface{}) bool {
	if obj == nil {
		return true
	}
	
	v := reflect.ValueOf(obj)
	switch v.Kind() {
	case reflect.Map, reflect.Slice:
		return v.Len() == 0
	case reflect.Ptr:
		if v.IsNil() {
			return true
		}
		return IsEmptyObject(v.Elem().Interface())
	case reflect.Struct:
		// 检查所有字段是否都是零值
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).IsZero() {
				return false
			}
		}
		return true
	default:
		return v.IsZero()
	}
}

// ParseSettlementContent 解析结算单内容
func ParseSettlementContent(txt string) *HisSettlement {
	if txt == "" {
		return &HisSettlement{}
	}
	
	lines := strings.Split(txt, "\n")
	currentSection := ""
	
	result := &HisSettlement{
		Account:            make(map[string]string),
		PositionClosed:     make([]map[string]string, 0),
		TransactionRecords: make([]map[string]string, 0),
	}
	
	// 表格状态
	tableStates := map[string]struct {
		title    string
		colNames []string
	}{
		"positionClosed": {
			title:    "平仓明细 Position Closed",
			colNames: []string{},
		},
		"transactionRecords": {
			title:    "成交记录 Transaction Record",
			colNames: []string{},
		},
	}
	
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		
		// 检查资金状况
		if strings.Contains(line, "资金状况") {
			currentSection = "account"
			i++
			continue
		}
		
		// 检查平仓明细或成交记录
		if strings.Contains(line, "平仓明细") || strings.Contains(line, "成交记录") {
			if strings.Contains(line, "平仓明细") {
				currentSection = "positionClosed"
			} else {
				currentSection = "transactionRecords"
			}
			
			// 读取表头
			for i++; i < len(lines); i++ {
				s := strings.TrimSpace(lines[i])
				if strings.Replace(s, "-", "", -1) == "" {
					if len(tableStates[currentSection].colNames) == 0 {
						continue
					} else {
						break
					}
				} else {
					state := tableStates[currentSection]
					state.colNames = genList(s)
					tableStates[currentSection] = state
				}
			}
			continue
		}
		
		// 处理账户信息
		if currentSection == "account" {
			if line == "" || strings.Replace(line, "-", "", -1) == "" {
				currentSection = ""
				continue
			}
			
			// 匹配英文标签和数字
			enRegex := regexp.MustCompile(`([A-Z][a-zA-Z\.\s/\(\)]+)[:：]+`)
			numRegex := regexp.MustCompile(`(-?[\d]+\.\d\d)`)
			
			enMatches := enRegex.FindAllStringSubmatch(line, -1)
			numMatches := numRegex.FindAllStringSubmatch(line, -1)
			
			for j := 0; j < len(enMatches) && j < len(numMatches); j++ {
				key := strings.Split(enMatches[j][1], ":")[0]
				key = strings.TrimSpace(key)
				result.Account[key] = numMatches[j][1]
			}
		} else if currentSection == "positionClosed" || currentSection == "transactionRecords" {
			// 处理平仓明细或成交记录
			if line == "" || strings.Replace(line, "-", "", -1) == "" {
				currentSection = ""
				continue
			}
			
			colNames := tableStates[currentSection].colNames
			contents := genList(line)
			data := genItem(colNames, contents)
			
			// 特殊处理成交记录中的手数和手续费
			if currentSection == "transactionRecords" && len(colNames) != len(contents) {
				indexLots := -1
				indexFee := -1
				for idx, name := range colNames {
					if name == "Lots" {
						indexLots = idx
					}
					if name == "Fee" {
						indexFee = idx
					}
				}
				
				if indexLots >= 0 && indexFee >= 0 {
					digitRegex := regexp.MustCompile(`^\d+$`)
					for i := 1; i < len(contents); i++ {
						if digitRegex.MatchString(contents[i]) {
							data["Lots"] = contents[i]
							if i+(indexFee-indexLots) < len(contents) {
								data["Fee"] = contents[i+(indexFee-indexLots)]
							}
							break
						}
					}
				}
			}
			
			if currentSection == "positionClosed" {
				result.PositionClosed = append(result.PositionClosed, data)
			} else {
				result.TransactionRecords = append(result.TransactionRecords, data)
			}
		}
	}
	
	return result
}

// genList 根据 | 分割字符串为数组
func genList(str string) []string {
	list := []string{}
	items := strings.Split(str, "|")
	
	for i, item := range items {
		name := strings.TrimSpace(item)
		// 去掉第一个和最后一个空白
		if i == 0 && name == "" {
			continue
		}
		if i == len(items)-1 && name == "" {
			continue
		}
		list = append(list, name)
	}
	
	return list
}

// genItem 根据 keys 和 values 生成 map
func genItem(keys []string, values []string) map[string]string {
	item := make(map[string]string)
	for j := 0; j < len(keys) && j < len(values); j++ {
		item[keys[j]] = values[j]
	}
	return item
}

// FetchJSON 从URL获取JSON数据
func FetchJSON(url string) (interface{}, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return result, nil
}

