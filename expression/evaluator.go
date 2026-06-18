package expression

import (
	"fmt"
	"strconv"
	"strings"
)

// Eval 计算表达式。expr 为 JSON 反序列化后的 any（map/array/string/number）。
// vars 为变量值映射，key 为变量名（不含 $ 前缀）。
// 返回计算结果。
func Eval(expr any, vars map[string]any) (any, error) {
	return evalAny(expr, vars)
}

func evalAny(expr any, vars map[string]any) (any, error) {
	switch v := expr.(type) {
	case map[string]any:
		return evalOp(v, vars)
	case []any:
		return evalArray(v, vars)
	case string:
		if strings.HasPrefix(v, "$") {
			return resolveVar(v[1:], vars)
		}
		return v, nil
	default:
		return v, nil
	}
}

// evalOp 计算单个操作符调用
func evalOp(op map[string]any, vars map[string]any) (any, error) {
	for name, args := range op {
		switch name {
		// -------- 比较 --------
		case "Eq":
			return cmpOp(args, vars, func(a, b float64) bool { return a == b }, func(a, b string) bool { return a == b })
		case "Neq":
			return cmpOp(args, vars, func(a, b float64) bool { return a != b }, func(a, b string) bool { return a != b })
		case "Gt":
			return numCmp(args, vars, func(a, b float64) bool { return a > b })
		case "Gte":
			return numCmp(args, vars, func(a, b float64) bool { return a >= b })
		case "Lt":
			return numCmp(args, vars, func(a, b float64) bool { return a < b })
		case "Lte":
			return numCmp(args, vars, func(a, b float64) bool { return a <= b })

		// -------- 逻辑 --------
		case "And":
			return logicOp(args, vars, true)
		case "Or":
			return logicOp(args, vars, false)
		case "Not":
			v, err := evalFirst(args, vars)
			if err != nil {
				return nil, err
			}
			b, _ := toBool(v)
			return !b, nil

		// -------- 算术 --------
		case "Plus":
			return arithOp(args, vars, func(a, b float64) float64 { return a + b })
		case "Minus":
			return arithOp(args, vars, func(a, b float64) float64 { return a - b })
		case "Multiply":
			return arithOp(args, vars, func(a, b float64) float64 { return a * b })
		case "Divide":
			return arithOp(args, vars, func(a, b float64) float64 {
				if b == 0 {
					return 0
				}
				return a / b
			})

		// -------- 字符串 --------
		case "Contains":
			return strOp(args, vars, strings.Contains)
		case "StartsWith":
			return strOp(args, vars, strings.HasPrefix)
		case "EndsWith":
			return strOp(args, vars, strings.HasSuffix)

		// -------- 集合 --------
		case "In":
			return inOp(args, vars)

		// -------- Switch/Case --------
		case "Switch":
			return switchEval(args, vars)
		case "Case":
			return caseEval(args, vars)
		case "Default":
			return evalFirst(args, vars)

		// -------- 类型转换 --------
		case "ToString":
			v, err := evalFirst(args, vars)
			if err != nil {
				return nil, err
			}
			return fmt.Sprint(v), nil
		case "ToNumber":
			v, err := evalFirst(args, vars)
			if err != nil {
				return nil, err
			}
			return toFloat(v)
		case "ToBool":
			v, err := evalFirst(args, vars)
			if err != nil {
				return nil, err
			}
			b, _ := toBool(v)
			return b, nil

		// -------- Null 检查 --------
		case "IsNull":
			v, err := evalFirst(args, vars)
			if err != nil {
				return nil, err
			}
			return v == nil || v == "", nil
		case "IsNotNull":
			v, err := evalFirst(args, vars)
			if err != nil {
				return nil, err
			}
			return v != nil && v != "", nil

		// -------- 默认值 --------
		case "Coalesce":
			for _, arg := range toArgs(args) {
				v, err := evalAny(arg, vars)
				if err == nil && v != nil && v != "" {
					return v, nil
				}
			}
			return nil, nil

		default:
			// 未知操作符 → 透传（自定义函数预留）
			result := make(map[string]any)
			evalArgs, err := evalArray(toArgs(args), vars)
			if err != nil {
				return nil, err
			}
			result[name] = evalArgs
			return result, nil
		}
	}
	return nil, fmt.Errorf("空操作符")
}

// ---------- helpers ----------

func toArgs(v any) []any {
	if arr, ok := v.([]any); ok {
		return arr
	}
	return nil
}

func evalFirst(args any, vars map[string]any) (any, error) {
	arr := toArgs(args)
	if len(arr) == 0 {
		return nil, fmt.Errorf("缺少参数")
	}
	return evalAny(arr[0], vars)
}

func evalTwo(args any, vars map[string]any) (any, any, error) {
	arr := toArgs(args)
	if len(arr) < 2 {
		return nil, nil, fmt.Errorf("需要2个参数")
	}
	a, err := evalAny(arr[0], vars)
	if err != nil {
		return nil, nil, err
	}
	b, err := evalAny(arr[1], vars)
	if err != nil {
		return nil, nil, err
	}
	return a, b, nil
}

func evalArray(args []any, vars map[string]any) ([]any, error) {
	result := make([]any, len(args))
	for i, a := range args {
		v, err := evalAny(a, vars)
		if err != nil {
			return nil, err
		}
		result[i] = v
	}
	return result, nil
}

func resolveVar(name string, vars map[string]any) (any, error) {
	if v, ok := vars[name]; ok {
		return v, nil
	}
	// 未找到变量 → 保持原样（支持 $now, $today 等系统变量，引擎层处理）
	return "$" + name, nil
}

// ---------- 类型转换 ----------

func toFloat(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case string:
		return strconv.ParseFloat(x, 64)
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("cannot convert to number: %v", v)
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case float64:
		return x != 0, nil
	case int:
		return x != 0, nil
	case string:
		return x != "" && x != "false" && x != "0", nil
	}
	return v != nil, nil
}

// ---------- 操作符实现 ----------

func cmpOp(args any, vars map[string]any, nf func(float64, float64) bool, sf func(string, string) bool) (bool, error) {
	a, b, err := evalTwo(args, vars)
	if err != nil {
		return false, err
	}
	// 尝试数值比较
	fa, ea := toFloat(a)
	fb, eb := toFloat(b)
	if ea == nil && eb == nil {
		return nf(fa, fb), nil
	}
	// 回退字符串比较
	return sf(fmt.Sprint(a), fmt.Sprint(b)), nil
}

func numCmp(args any, vars map[string]any, f func(float64, float64) bool) (bool, error) {
	a, b, err := evalTwo(args, vars)
	if err != nil {
		return false, err
	}
	fa, err := toFloat(a)
	if err != nil {
		return false, fmt.Errorf("比较需要数字: %v", err)
	}
	fb, err := toFloat(b)
	if err != nil {
		return false, fmt.Errorf("比较需要数字: %v", err)
	}
	return f(fa, fb), nil
}

func logicOp(args any, vars map[string]any, isAnd bool) (bool, error) {
	arr := toArgs(args)
	for _, arg := range arr {
		v, err := evalAny(arg, vars)
		if err != nil {
			return false, err
		}
		b, _ := toBool(v)
		if isAnd && !b {
			return false, nil
		}
		if !isAnd && b {
			return true, nil
		}
	}
	return isAnd, nil
}

func arithOp(args any, vars map[string]any, f func(float64, float64) float64) (float64, error) {
	a, b, err := evalTwo(args, vars)
	if err != nil {
		return 0, err
	}
	fa, err := toFloat(a)
	if err != nil {
		return 0, err
	}
	fb, err := toFloat(b)
	if err != nil {
		return 0, err
	}
	return f(fa, fb), nil
}

func strOp(args any, vars map[string]any, f func(string, string) bool) (bool, error) {
	a, b, err := evalTwo(args, vars)
	if err != nil {
		return false, err
	}
	return f(fmt.Sprint(a), fmt.Sprint(b)), nil
}

func inOp(args any, vars map[string]any) (bool, error) {
	arr := toArgs(args)
	if len(arr) < 2 {
		return false, fmt.Errorf("In 需要2+参数")
	}
	val, err := evalAny(arr[0], vars)
	if err != nil {
		return false, err
	}
	for _, arg := range arr[1:] {
		switch x := arg.(type) {
		case []any:
			for _, item := range x {
				v, _ := evalAny(item, vars)
				if fmt.Sprint(v) == fmt.Sprint(val) {
					return true, nil
				}
			}
		default:
			v, _ := evalAny(arg, vars)
			if fmt.Sprint(v) == fmt.Sprint(val) {
				return true, nil
			}
		}
	}
	return false, nil
}

func switchEval(args any, vars map[string]any) (any, error) {
	arr := toArgs(args)
	for _, arg := range arr {
		v, err := evalAny(arg, vars)
		if err != nil {
			return nil, err
		}
		if v != nil {
			return v, nil
		}
	}
	return nil, nil
}

func caseEval(args any, vars map[string]any) (any, error) {
	arr := toArgs(args)
	if len(arr) < 2 {
		return nil, nil
	}
	cond, err := evalAny(arr[0], vars)
	if err != nil {
		return nil, err
	}
	b, _ := toBool(cond)
	if b {
		return evalAny(arr[1], vars)
	}
	return nil, nil // false → Switch 继续下一个 Case
}
